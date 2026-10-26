package dumpfile

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLine(t *testing.T) {
	const max = 16
	long := strings.Repeat("x", max+10)
	br := bufio.NewReaderSize(strings.NewReader("a\n"+long+"\nb\n"), 16)
	for _, want := range []struct {
		line    string
		tooLong bool
	}{{"a", false}, {"", true}, {"b", false}} {
		line, tooLong, err := ReadLine(br, max)
		if err != nil || string(line) != want.line || tooLong != want.tooLong {
			t.Fatalf("ReadLine = %q %v %v, ждали %q %v", line, tooLong, err, want.line, want.tooLong)
		}
	}
	if _, _, err := ReadLine(br, max); !errors.Is(err, io.EOF) {
		t.Fatalf("конец потока: %v", err)
	}
	// Недокачанный файл кончается посреди строки — это обрыв, а не конец
	br = bufio.NewReader(strings.NewReader("a\n{\"word\""))
	if _, _, err := ReadLine(br, max); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadLine(br, max); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("обрыв: %v", err)
	}
}

// bzip2Line — "line\n", сжатое `bzip2 -9`: в стандартной библиотеке
// Go есть только распаковщик bzip2.
var bzip2Line = []byte{0x42, 0x5a, 0x68, 0x39, 0x31, 0x41, 0x59, 0x26, 0x53, 0x59, 0x5c, 0x5a,
	0x44, 0xcd, 0x00, 0x00, 0x01, 0xc1, 0x00, 0x00, 0x10, 0x02, 0x25, 0x20, 0x00, 0x30, 0xcd,
	0x00, 0xc1, 0xa4, 0x0c, 0x71, 0x77, 0x24, 0x53, 0x85, 0x09, 0x05, 0xc5, 0xa4, 0x4c, 0xd0}

func TestDecompress(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("line\n"))
	_ = zw.Close()
	for name, src := range map[string]io.Reader{
		"gzip":  &gz,
		"bzip2": bytes.NewReader(bzip2Line),
		"plain": strings.NewReader("line\n"),
	} {
		r, err := Decompress(src)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if err != nil || string(got) != "line\n" {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	for _, short := range []string{"", "a"} {
		r, err := Decompress(strings.NewReader(short))
		if err != nil {
			t.Fatalf("источник %q: %v", short, err)
		}
		if got, _ := io.ReadAll(r); string(got) != short {
			t.Fatalf("источник %q: %q", short, got)
		}
	}
	// Оборванный bzip2 — ошибка чтения, а не тихий конец данных
	r, err := Decompress(bytes.NewReader(bzip2Line[:len(bzip2Line)-8]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("оборванный bzip2 прочитан без ошибки")
	}
}

// Версия локального файла — размер и mtime; http без TLS не принимается.
func TestOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dump.tsv")
	if err := os.WriteFile(path, []byte("1\t2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, version, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !strings.HasPrefix(version, "size=4 mtime=") {
		t.Fatalf("версия файла %q", version)
	}
	if _, _, err := Open(context.Background(), "http://example.com/dump.tsv"); err == nil {
		t.Fatal("http-источник принят")
	}
	if _, _, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("несуществующий файл открыт")
	}
}
