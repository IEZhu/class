package cloze

import (
	"reflect"
	"slices"
	"testing"

	"github.com/IEZhu/class/backend/internal/lemma"
)

func words(lemmas ...string) []Word {
	out := make([]Word, 0, len(lemmas))
	for _, l := range lemmas {
		out = append(out, Word{Lemma: l})
	}
	return out
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

// Дистракторы — из словаря студента той же части речи: сначала точный
// набор частей речи (banana — только существительное), затем
// пересекающийся (orange, run); цель, её формы, служебные слова и слова
// предложения (bread) не годятся; наречие и прилагательное к
// существительному не идут.
func TestPickDistractorsSamePOSFromList(t *testing.T) {
	for w, pos := range map[string]string{"apple": "n", "banana": "n", "quickly": "r", "happy": "a"} {
		if got := lemma.POS(w); got != pos {
			t.Fatalf("фикстура: POS(%q) = %q, а не %q", w, got, pos)
		}
	}
	target := Target{Lemma: "apple", Surface: "apples"}
	pool := words("apple", "banana", "bread", "orange", "run", "quickly", "happy", "the")
	sentence := "I eat apples with bread every day."
	seed := Hash(sentence, Blank{Start: 6, Len: 6})
	got := PickDistractors(target, pool, sentence, seed)
	if len(got) != Distractors || got[0] != "banana" {
		t.Fatalf("дистракторы %q: первым — banana", got)
	}
	if want := []string{"banana", "orange", "run"}; !reflect.DeepEqual(sorted(got), want) {
		t.Fatalf("дистракторы %q, хотели %q", got, want)
	}
	// Детерминизм: тот же пункт — те же варианты в том же порядке
	if again := PickDistractors(target, pool, sentence, seed); !reflect.DeepEqual(got, again) {
		t.Fatalf("повтор дал %q вместо %q", again, got)
	}
}

// Слова того же урока, что и цель, идут раньше остальных той же ступени.
func TestPickDistractorsSameLessonFirst(t *testing.T) {
	lesson, other := int64(7), int64(8)
	target := Target{Lemma: "apple", Surface: "apple", LessonID: &lesson}
	pool := []Word{
		{Lemma: "banana", LessonID: &other}, {Lemma: "lemon", LessonID: &other}, {Lemma: "cherry"},
		{Lemma: "carrot", LessonID: &lesson}, {Lemma: "potato", LessonID: &lesson}, {Lemma: "melon"},
	}
	got := PickDistractors(target, pool, "I want an apple for lunch.", []byte("seed"))
	if want := []string{"carrot", "potato"}; !reflect.DeepEqual(sorted(got[:2]), want) {
		t.Fatalf("дистракторы %q: первыми слова урока %q", got, want)
	}
}

// Словаря не хватает — частые слова той же части речи; слова
// предложения и цель среди них не появляются.
func TestPickDistractorsFallsBackToFrequentWords(t *testing.T) {
	target := Target{Lemma: "apple", Surface: "apple"}
	sentence := "Every day after school I eat an apple."
	got := PickDistractors(target, words("banana"), sentence, []byte("seed"))
	if len(got) != Distractors || got[0] != "banana" {
		t.Fatalf("дистракторы %q: сначала словарь", got)
	}
	for _, w := range got[1:] {
		if !slices.Contains(frequentWords, w) || !sharesPOS(lemma.POS(w), "n") {
			t.Errorf("запасной дистрактор %q не частое существительное", w)
		}
		if w == "day" || w == "school" {
			t.Errorf("дистрактор %q есть в предложении", w)
		}
	}
	// Пустой словарь и неизвестная часть речи цели — всё равно три
	unknown := PickDistractors(Target{Lemma: "blorpy", Surface: "blorpy"}, nil, "The blorpy sat on the mat.", []byte("x"))
	if len(unknown) != Distractors {
		t.Fatalf("без словаря и части речи: %q", unknown)
	}
}

// Часть речи цели неизвестна (слова нет в OEWN) — годится словарь
// целиком, наречие к нему тоже; служебные слова (the) не идут и тогда:
// третий вариант — из частых слов, а не the.
func TestPickDistractorsUnknownPOSUsesWholeList(t *testing.T) {
	if lemma.POS("zorbix") != "" || lemma.POS("quickly") != "r" || !lemma.FunctionWord("the") {
		t.Fatal("фикстура: zorbix вне OEWN, quickly — наречие, the — служебное")
	}
	target := Target{Lemma: "zorbix", Surface: "zorbix"}
	sentence := "My brother bought a zorbix yesterday."
	got := PickDistractors(target, words("banana", "quickly", "the"), sentence, []byte("seed"))
	if len(got) != Distractors || !slices.Contains(got, "banana") || !slices.Contains(got, "quickly") {
		t.Fatalf("дистракторы %q: весь словарь при неизвестной части речи", got)
	}
	if slices.Contains(got, "the") || !slices.Contains(frequentWords, got[2]) {
		t.Fatalf("дистракторы %q: служебное слово вместо частого", got)
	}
}

// Ключ цели не из одних латинских букв (t-shirt, café): среди
// дистракторов есть такой же непохожий из словаря — ответ не единственная
// кнопка с дефисом или диакритикой; нет такого слова — вариантов нет.
// Обычной цели непохожие слова словаря по-прежнему годятся.
func TestPickDistractorsOddShapedTarget(t *testing.T) {
	if lemma.POS("t-shirt") != "n" || lemma.POS("café") != "" || !sharesPOS(lemma.POS("x-ray"), "n") {
		t.Fatal("фикстура: t-shirt — существительное, café вне OEWN, x-ray — и существительное")
	}
	pool := words("coat", "jacket", "sweater", "shop", "x-ray", "t-shirt", "café")
	cases := []struct{ lemma, sentence string }{
		{"t-shirt", "He wore a white t-shirt to school today."},
		{"café", "We met at a small café near the station."},
	}
	for _, c := range cases {
		target := Target{Lemma: c.lemma, Surface: c.lemma}
		seed := []byte(c.sentence)
		got := PickDistractors(target, pool, c.sentence, seed)
		if len(got) != Distractors || letters(got[0]) {
			t.Fatalf("%s: дистракторы %q — первым нужен непохожий", c.lemma, got)
		}
		if !reflect.DeepEqual(got, PickDistractors(target, pool, c.sentence, seed)) {
			t.Fatalf("%s: выбор не детерминирован", c.lemma)
		}
		if lonely := PickDistractors(target, words("coat", "jacket", "sweater", "shop"), c.sentence, seed); lonely != nil {
			t.Fatalf("%s без непохожих слов в словаре: %q", c.lemma, lonely)
		}
	}
	plain := PickDistractors(Target{Lemma: "coat", Surface: "coat"}, words("x-ray", "t-shirt"), "She took off her warm coat.", []byte("s"))
	if len(plain) != Distractors || !slices.Contains(plain, "x-ray") || !slices.Contains(plain, "t-shirt") {
		t.Fatalf("обычная цель: %q", plain)
	}
}

// Варианты: ответ ровно один раз, порядок стабилен у пункта и меняется
// между пунктами — ответ не стоит всегда на одном месте.
func TestOptionsShuffleDeterministically(t *testing.T) {
	distractors := []string{"walk", "swim", "jump"}
	positions := map[int]bool{}
	for _, seed := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		opts := Options("Run", distractors, []byte(seed))
		if len(opts) != 4 || !reflect.DeepEqual(opts, Options("Run", distractors, []byte(seed))) {
			t.Fatalf("варианты %q не стабильны", opts)
		}
		if n := countOf(opts, "run"); n != 1 {
			t.Fatalf("ответ в вариантах %d раз: %q", n, opts)
		}
		positions[slices.Index(opts, "run")] = true
	}
	if len(positions) < 2 {
		t.Fatalf("ответ всегда на месте %v", positions)
	}
}

func countOf(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}

// Запас частых слов: каждое — лемма OEWN с известной частью речи, не
// служебное, без повторов.
func TestFrequentWords(t *testing.T) {
	seen := map[string]bool{}
	for _, w := range frequentWords {
		if seen[w] {
			t.Errorf("повтор %q", w)
		}
		seen[w] = true
		if !distractor(w) || lemma.Lemma(w) != w || lemma.POS(w) == "" {
			t.Errorf("%q: не годится в запас (лемма %q, части речи %q)", w, lemma.Lemma(w), lemma.POS(w))
		}
	}
	for _, pos := range []string{"n", "v", "a", "r"} {
		n := 0
		for _, w := range frequentWords {
			if sharesPOS(lemma.POS(w), pos) {
				n++
			}
		}
		if n < 10 {
			t.Errorf("частых слов с частью речи %s: %d", pos, n)
		}
	}
}
