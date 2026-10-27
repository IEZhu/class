package dumpfile

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

// Lazy знает версию до открытия, открывает файл при первом чтении и не
// читает файл, сменившийся после начала импорта.
func TestLazy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dump.tsv")
	if err := os.WriteFile(path, []byte("1\t2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLazy(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if !strings.HasPrefix(l.Version(), "size=4 mtime=") {
		t.Fatalf("версия до открытия %q", l.Version())
	}
	if l.rc != nil {
		t.Fatal("файл открыт до первого чтения")
	}
	if got, err := io.ReadAll(l); err != nil || string(got) != "1\t2\n" {
		t.Fatalf("чтение: %q, %v", got, err)
	}

	changed, err := OpenLazy(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer changed.Close()
	if err := os.WriteFile(path, []byte("1\t2\n3\t4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := changed.Read(make([]byte, 8)); !errors.Is(err, ErrChanged) {
		t.Fatalf("файл сменился после OpenLazy: %v", err)
	}

	if _, err := OpenLazy(ctx, "http://example.com/dump.tsv"); err == nil {
		t.Fatal("http-источник принят")
	}
	if _, err := OpenLazy(ctx, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("версия несуществующего файла")
	}
}

// trustTestCert — клиент выгрузок до конца теста доверяет сертификату
// httptest: он один у всех TLS-серверов httptest. Меняется только TLS
// транспорта, проверка переадресаций — та же, что в работе.
func trustTestCert(t *testing.T, srv *httptest.Server) {
	t.Helper()
	tr := client.Transport.(*http.Transport)
	prev := tr.TLSClientConfig
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	t.Cleanup(func() {
		tr.CloseIdleConnections()
		tr.TLSClientConfig = prev
	})
}

// Переадресация, как и сам источник, — только на https (ревью PR #17):
// https → https читается, https → http — ошибка, а открытый адрес даже
// не запрашивается; потолок — 10 переадресаций, как у http.Client.
func TestRedirectsHTTPSOnly(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		fmt.Fprint(w, "1\t2\n")
	}))
	defer plain.Close()
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, "1\t2\n")
	}))
	defer target.Close()
	var loops atomic.Int32
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/secure":
			http.Redirect(w, r, target.URL+"/dump.tsv", http.StatusFound)
		case "/downgrade":
			http.Redirect(w, r, plain.URL+"/dump.tsv", http.StatusFound)
		case "/loop":
			loops.Add(1)
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer redirector.Close()
	trustTestCert(t, redirector)
	ctx := context.Background()

	rc, version, err := Open(ctx, redirector.URL+"/secure")
	if err != nil {
		t.Fatalf("https → https: %v", err)
	}
	body, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(body) != "1\t2\n" || version != `"v1"` {
		t.Fatalf("https → https: %q, версия %q, %v", body, version, err)
	}
	if version, err := Stat(ctx, redirector.URL+"/secure"); err != nil || version != `"v1"` {
		t.Fatalf("HEAD https → https: %q, %v", version, err)
	}

	if _, _, err := Open(ctx, redirector.URL+"/downgrade"); err == nil || !strings.Contains(err.Error(), "только https") {
		t.Fatalf("GET https → http: %v, ждали отказ", err)
	}
	if _, err := Stat(ctx, redirector.URL+"/downgrade"); err == nil || !strings.Contains(err.Error(), "только https") {
		t.Fatalf("HEAD https → http: %v, ждали отказ", err)
	}
	if n := plainHits.Load(); n != 0 {
		t.Fatalf("по http ушло %d запросов", n)
	}

	if _, _, err := Open(ctx, redirector.URL+"/loop"); err == nil || !strings.Contains(err.Error(), "больше 10 переадресаций") {
		t.Fatalf("петля переадресаций: %v", err)
	}
	if n := loops.Load(); n != maxRedirects {
		t.Fatalf("по петле сделано %d запросов, ждали %d", n, maxRedirects)
	}
}

// Тело ответа без данных дольше bodyIdleTimeout (ревью PR #17): источник
// отдал заголовки и начало файла и замолчал — чтение возвращает ошибку
// по таймауту простоя, а не висит; живая загрузка, которая в сумме
// дольше таймаута, но присылает данные чаще, доходит до конца.
func TestBodyIdleTimeout(t *testing.T) {
	prev := bodyIdleTimeout
	bodyIdleTimeout = 500 * time.Millisecond
	t.Cleanup(func() { bodyIdleTimeout = prev })
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stall":
			fmt.Fprint(w, "1\t2\n")
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case "/slow":
			for i := range 8 {
				fmt.Fprintf(w, "%d\t%d\n", i, i)
				w.(http.Flusher).Flush()
				time.Sleep(100 * time.Millisecond)
			}
		}
	}))
	defer srv.Close()
	defer close(release)
	trustTestCert(t, srv)
	ctx := context.Background()

	rc, _, err := Open(ctx, srv.URL+"/stall")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	body, err := io.ReadAll(rc)
	rc.Close()
	if elapsed := time.Since(start); !errors.Is(err, errStalled) || string(body) != "1\t2\n" ||
		elapsed < bodyIdleTimeout || elapsed > 30*time.Second {
		t.Fatalf("замолчавший источник: %q, %v за %s", body, err, elapsed)
	}

	rc, _, err = Open(ctx, srv.URL+"/slow")
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	body, err = io.ReadAll(rc)
	rc.Close()
	if elapsed := time.Since(start); err != nil || strings.Count(string(body), "\n") != 8 || elapsed < bodyIdleTimeout {
		t.Fatalf("медленная загрузка: %q, %v за %s", body, err, elapsed)
	}
}

// Таймаут простоя считает только ожидание внутри Read (ревью PR #18):
// пауза между чтениями — импорт пишет пачку в базу — дольше
// bodyIdleTimeout не обрывает тело, в котором ещё есть данные.
func TestBodyIdleNotSpentBetweenReads(t *testing.T) {
	prev := bodyIdleTimeout
	bodyIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { bodyIdleTimeout = prev })
	want := strings.Repeat("1\t2\n", 4)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, want)
	}))
	defer srv.Close()
	trustTestCert(t, srv)

	rc, _, err := Open(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(rc, head); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * bodyIdleTimeout)
	rest, err := io.ReadAll(rc)
	if err != nil || string(head)+string(rest) != want {
		t.Fatalf("после паузы между чтениями: %q %q, %v", head, rest, err)
	}
}
