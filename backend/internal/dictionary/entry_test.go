package dictionary

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

var testLimits = Limits{MaxGlosses: DefaultMaxGlosses, MaxTranslations: DefaultMaxTranslations}

// Фикстура — строки в формате Wiktextract с выдуманными значениями;
// разбор оставляет только то, что нужно попапу (ADR-019).
func TestParseFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/wiktextract-sample.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var kept []Entry
	malformed := 0
	for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		e, ok, err := parseEntry(line, testLimits)
		if err != nil {
			malformed++
			continue
		}
		if ok {
			kept = append(kept, e)
		}
	}
	if malformed != 1 {
		t.Fatalf("испорченных строк %d, ждали 1", malformed)
	}
	var got []string
	for _, e := range kept {
		got = append(got, e.Word+"/"+e.Pos)
	}
	// ran — только ссылка на форму, livre — французский, rain cats and
	// dogs — словосочетание, zzyzxq — нет в OEWN, thou — только archaic
	want := "run/verb run/noun the/article bank/noun bank/noun building/noun run/verb polish/verb Polish/adj"
	if strings.Join(got, " ") != want {
		t.Fatalf("оставлены %s, ждали %s", strings.Join(got, " "), want)
	}
	run := kept[0]
	if !reflect.DeepEqual(run, Entry{
		Word: "run", Lemma: "run", Pos: "verb", Etymology: 1,
		// третье значение obsolete выброшено, у подзначения — его собственный текст
		Glosses: []string{"To move swiftly on foot.", "To go somewhere quickly for a short time.",
			"To manage or be in charge of."},
		// ударения сняты, повтор и немецкий перевод отброшены
		TranslationsRU: []string{"бегать", "бежать"},
	}) {
		t.Fatalf("run/verb = %+v", run)
	}
	if kept[1].Etymology != 1 || kept[3].Etymology != 1 || kept[4].Etymology != 2 || kept[2].Etymology != 0 {
		t.Fatal("номер этимологии и строкой, и числом")
	}
	if kept[8].Lemma != "polish" || kept[8].TranslationsRU[0] != "польский" {
		t.Fatalf("Polish = %+v", kept[8])
	}
}

func TestParseLimits(t *testing.T) {
	line := []byte(`{"word":"set","lang_code":"en","pos":"verb","senses":[` +
		`{"glosses":["one"]},{"glosses":["two"]},{"glosses":["two"]},{"glosses":["` + strings.Repeat("x", 400) + `"]}],` +
		`"translations":[{"code":"ru","word":"а"},{"code":"ru","word":"б"},{"code":"ru","word":"в"}]}`)
	e, ok, err := parseEntry(line, Limits{MaxGlosses: 3, MaxTranslations: 2})
	if err != nil || !ok {
		t.Fatalf("parse: %v %v", ok, err)
	}
	if len(e.Glosses) != 3 || e.Glosses[1] != "two" || len([]rune(e.Glosses[2])) != maxGlossRunes ||
		!strings.HasSuffix(e.Glosses[2], "…") {
		t.Fatalf("значения %q", e.Glosses)
	}
	if !reflect.DeepEqual(e.TranslationsRU, []string{"а", "б"}) {
		t.Fatalf("переводы %q", e.TranslationsRU)
	}
}

func TestReadLine(t *testing.T) {
	long := strings.Repeat("x", maxLineBytes+10)
	br := bufio.NewReaderSize(strings.NewReader("a\n"+long+"\nb\n"), 1<<20)
	for _, want := range []struct {
		line    string
		tooLong bool
	}{{"a", false}, {"", true}, {"b", false}} {
		line, tooLong, err := readLine(br)
		if err != nil || string(line) != want.line || tooLong != want.tooLong {
			t.Fatalf("readLine = %q %v %v, ждали %q %v", line, tooLong, err, want.line, want.tooLong)
		}
	}
	if _, _, err := readLine(br); !errors.Is(err, io.EOF) {
		t.Fatalf("конец потока: %v", err)
	}
	// Недокачанный файл кончается посреди строки — это обрыв, а не конец
	br = bufio.NewReader(strings.NewReader("a\n{\"word\""))
	if _, _, err := readLine(br); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLine(br); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("обрыв: %v", err)
	}
}

func TestDecompress(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("line\n"))
	_ = zw.Close()
	for name, src := range map[string]io.Reader{
		"gzip":  &gz,
		"plain": strings.NewReader("line\n"),
	} {
		r, err := decompress(src)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if err != nil || string(got) != "line\n" {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	if r, err := decompress(strings.NewReader("")); err != nil {
		t.Fatalf("пустой источник: %v", err)
	} else if got, _ := io.ReadAll(r); len(got) != 0 {
		t.Fatalf("пустой источник: %q", got)
	}
}
