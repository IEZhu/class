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
