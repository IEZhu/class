package lemma

import (
	"reflect"
	"testing"
)

func TestWords(t *testing.T) {
	got := Words("Don’t stop: the teachers' well-known café—R2D2 isn't 42 years-old! -run- a--b")
	want := []string{"Don’t", "stop", "the", "teachers", "well-known", "café", "R", "D", "isn't",
		"years-old", "run", "a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("токены %q\nхотели %q", got, want)
	}
}

// Места слов — в рунах и ровно те же слова, что у Words: cloze режет
// по ним предложение.
func TestWordSpans(t *testing.T) {
	text := "Café’s day—well-known, isn't it?"
	spans := WordSpans(text)
	rs := []rune(text)
	var got []string
	for _, sp := range spans {
		got = append(got, string(rs[sp.Start:sp.End]))
	}
	if want := Words(text); !reflect.DeepEqual(got, want) {
		t.Fatalf("по местам %q, Words %q", got, want)
	}
	if want := []Span{{0, 6}, {7, 10}, {11, 21}, {23, 28}, {29, 31}}; !reflect.DeepEqual(spans, want) {
		t.Fatalf("места %v, хотели %v", spans, want)
	}
}
