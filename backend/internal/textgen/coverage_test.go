package textgen

import (
	"math"
	"reflect"
	"testing"
)

func targets(t *testing.T, words ...string) []Target {
	t.Helper()
	p, err := NewParams(words, "B1", "", 400)
	if err != nil {
		t.Fatal(err)
	}
	return p.Targets
}

// Формы слова засчитываются его цели через лемму или форму: ran и runs —
// run, buildings — building (её лемма build), well-known — known.
func TestCheckCountsLemmatizedForms(t *testing.T) {
	ts := targets(t, "run", "building", "known", "mouse")
	text := "Tom ran to the old buildings. He runs every day past a building.\n\n" +
		"It is a well-known place, and he has known it for years. Mice live there; a mouse ran."
	rep := Check(text, ts)
	want := map[string]int{"run": 3, "build": 2, "know": 2, "mouse": 2}
	if !reflect.DeepEqual(rep.Occurrences, want) {
		t.Fatalf("вхождения %v, хотели %v", rep.Occurrences, want)
	}
	if !rep.Passed() || rep.Coverage != 1 || len(rep.Missing) != 0 {
		t.Fatalf("отчёт %+v", rep)
	}
	if rep.Words != 31 {
		t.Errorf("слов %d, хотели 31", rep.Words)
	}
}

// Цель, встреченная один раз или ни разу, — в Missing; покрытие — доля
// целей с двумя вхождениями. Ниже порога — Passed false: нужен ретрай.
func TestCheckCoverageAndRetryDecision(t *testing.T) {
	ts := targets(t, "cat", "dog", "bird", "fish")
	for _, c := range []struct {
		text     string
		coverage float64
		missing  []string
		passed   bool
	}{
		{"A cat and a dog. The cat sees the dog.", 0.5, []string{"bird", "fish"}, false},
		{"A cat, cats, a dog, dogs, a bird, birds and one fish.", 0.75, []string{"fish"}, false},
		{"Cats, a cat, dogs, a dog, birds, a bird, fish and more fish.", 1, nil, true},
		{"", 0, []string{"cat", "dog", "bird", "fish"}, false},
	} {
		rep := Check(c.text, ts)
		if rep.Coverage != c.coverage || !reflect.DeepEqual(rep.Missing, c.missing) || rep.Passed() != c.passed {
			t.Errorf("%q: покрытие %v, нет %v, прошёл %v; хотели %v, %v, %v",
				c.text, rep.Coverage, rep.Missing, rep.Passed(), c.coverage, c.missing, c.passed)
		}
	}
}

// Доля слов вне словаря: служебные слова (включая сокращения it's,
// don't), цели и слова OEWN не считаются; имена и выдумки — считаются.
func TestCheckUnknownShare(t *testing.T) {
	ts := targets(t, "zorblat")
	for _, c := range []struct {
		text string
		want float64
	}{
		{"The cat is on the mat and it's happy, isn't it? They don't know why.", 0},
		{"Zorblat saw a zorblat.", 0},
		{"Kevork met Ashkhen and they're glad.", 2.0 / 6},
		{"Blorpy flimwax.", 1},
		{"A well-known well-blorped man.", 1.0 / 4},
	} {
		got := Check(c.text, ts).UnknownShare
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%q: доля вне словаря %v, хотели %v", c.text, got, c.want)
		}
	}
}

func TestTokenize(t *testing.T) {
	got := tokenize("Don’t stop: the teachers' well-known café—R2D2 isn't 42 years-old! -run- a--b")
	want := []string{"Don’t", "stop", "the", "teachers", "well-known", "café", "R", "D", "isn't",
		"years-old", "run", "a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("токены %q\nхотели %q", got, want)
	}
}
