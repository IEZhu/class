package webvtt

import (
	"regexp"
	"strings"
	"testing"
)

// Эталон целиком: заголовок, пустая строка перед каждым cue, тайминги
// HH:MM:SS.mmm и голос спикера.
func TestRenderShape(t *testing.T) {
	got := string(Render([]Cue{
		{StartMS: 0, EndMS: 1500, Voice: "A", Text: "Hello, class."},
		{StartMS: 1500, EndMS: 3_723_004, Voice: "B", Text: "Hi!"},
	}))
	want := "WEBVTT\n" +
		"\n00:00:00.000 --> 00:00:01.500\n<v A>Hello, class.\n" +
		"\n00:00:01.500 --> 01:02:03.004\n<v B>Hi!\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderEmpty(t *testing.T) {
	if got := string(Render(nil)); got != "WEBVTT\n" {
		t.Fatalf("пустой транскрипт = %q, хотели один заголовок", got)
	}
}

// Текст реплики — данные провайдера: разметку из него браузер не должен
// принять за теги, а «-->» — за тайминг.
func TestRenderEscapes(t *testing.T) {
	got := string(Render([]Cue{{StartMS: 0, EndMS: 10, Voice: "A>B & <C>", Text: `x < y && "a --> b" <i>`}}))
	want := "<v A&gt;B &amp; &lt;C&gt;>x &lt; y &amp;&amp; \"a --&gt; b\" &lt;i&gt;\n"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("got %q, хотели окончание %q", got, want)
	}
	if strings.Count(got, "-->") != 1 {
		t.Fatalf("«-->» встречается вне тайминга: %q", got)
	}
}

// Пустая строка внутри cue оборвала бы его: остаток стал бы мусором.
func TestRenderJoinsLines(t *testing.T) {
	got := string(Render([]Cue{{StartMS: 0, EndMS: 10, Voice: "A\nB", Text: "first\n\nsecond\r\n  third "}}))
	if !strings.HasSuffix(got, "<v A B>first second third\n") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(strings.TrimPrefix(got, "WEBVTT\n\n"), "\n\n") {
		t.Fatalf("внутри cue пустая строка: %q", got)
	}
}

// Без метки спикера голос не пишется вовсе: `<v >` — невалидный тег.
func TestRenderWithoutVoice(t *testing.T) {
	got := string(Render([]Cue{{StartMS: 0, EndMS: 10, Text: "narration"}}))
	if !strings.HasSuffix(got, "\nnarration\n") || strings.Contains(got, "<v") {
		t.Fatalf("got %q", got)
	}
}

// Порядок по началу, при равном начале — исходный; конец строго позже
// начала, отрицательное время обнуляется.
func TestRenderOrdersAndFixesTimings(t *testing.T) {
	got := string(Render([]Cue{
		{StartMS: 5000, EndMS: 6000, Voice: "C", Text: "third"},
		{StartMS: 1000, EndMS: 1000, Voice: "A", Text: "first"},
		{StartMS: 1000, EndMS: 900, Voice: "B", Text: "second"},
		{StartMS: -20, EndMS: -10, Voice: "Z", Text: "zero"},
	}))
	want := "WEBVTT\n" +
		"\n00:00:00.000 --> 00:00:00.001\n<v Z>zero\n" +
		"\n00:00:01.000 --> 00:00:01.001\n<v A>first\n" +
		"\n00:00:01.000 --> 00:00:01.001\n<v B>second\n" +
		"\n00:00:05.000 --> 00:00:06.000\n<v C>third\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Структурная проверка по грамматике WebVTT: заголовок, затем блоки
// «тайминг + одна строка текста», разделённые ровно одной пустой строкой.
func TestRenderGrammar(t *testing.T) {
	out := string(Render([]Cue{
		{StartMS: 0, EndMS: 999, Voice: "A", Text: "one"},
		{StartMS: 59_999, EndMS: 60_000, Voice: "B", Text: "two"},
		{StartMS: 359_999_999, EndMS: 360_000_000, Voice: "C", Text: "three"},
	}))
	blocks := strings.Split(strings.TrimSuffix(out, "\n"), "\n\n")
	if blocks[0] != "WEBVTT" {
		t.Fatalf("заголовок %q", blocks[0])
	}
	timing := regexp.MustCompile(`^(\d{2,}):([0-5]\d):([0-5]\d)\.(\d{3}) --> (\d{2,}):([0-5]\d):([0-5]\d)\.(\d{3})$`)
	for _, block := range blocks[1:] {
		lines := strings.Split(block, "\n")
		if len(lines) != 2 || !timing.MatchString(lines[0]) || !strings.HasPrefix(lines[1], "<v ") {
			t.Fatalf("блок не по грамматике: %q", block)
		}
	}
	if !strings.Contains(out, "\n99:59:59.999 --> 100:00:00.000\n") {
		t.Fatalf("часы за 99 не уместились: %q", out)
	}
}
