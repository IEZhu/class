package cloze

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/IEZhu/class/backend/internal/lemma"
	"github.com/IEZhu/class/backend/internal/srs"
)

// Материал урока в markdown: заголовок, список, цитата, нумерация —
// предложения без маркеров, разрезанные по знакам конца; пустые строки
// и пробелы не дают пустых предложений.
func TestSentences(t *testing.T) {
	text := "# Lesson 3\n\n- We went to the park. It was   sunny!\n> 2) Did you see \"the dog?\" Yes.\n\nNo end"
	want := []string{"Lesson 3", "We went to the park.", "It was sunny!", `Did you see "the dog?"`, "Yes.", "No end"}
	if got := Sentences(text); !reflect.DeepEqual(got, want) {
		t.Fatalf("предложения %q\nхотели %q", got, want)
	}
}

// Пропуск — на месте формы слова: неправильной (ran), с заглавной
// (Running), без притяжательного 's. Ответа больше нигде в предложении
// нет — ни вторым вхождением, ни частью составного слова.
func TestFindBlanksSurfaceForm(t *testing.T) {
	for _, tc := range []struct {
		sentence, target       string
		before, surface, after string
	}{
		{"He ran home after the long lesson.", "run", "He ", "ran", " home after the long lesson."},
		{"Running every morning keeps me happy.", "run", "", "Running", " every morning keeps me happy."},
		{"The student's book is on the table.", "student", "The ", "student", "'s book is on the table."},
		{"We waited for the bus in the rain.", "wait", "We ", "waited", " for the bus in the rain."},
		{"Café owners open their doors early.", "café", "", "Café", " owners open their doors early."},
	} {
		b, ok := Find(tc.sentence, tc.target)
		if !ok {
			t.Errorf("%q: пропуск для %q не найден", tc.sentence, tc.target)
			continue
		}
		before, surface, after := Parts(tc.sentence, b)
		if before != tc.before || surface != tc.surface || after != tc.after {
			t.Errorf("%q: %q|%q|%q, хотели %q|%q|%q", tc.sentence, before, surface, after, tc.before, tc.surface, tc.after)
		}
		if !b.Valid(tc.sentence) {
			t.Errorf("%q: пропуск %+v вне предложения", tc.sentence, b)
		}
		// Нет утечки: ни одно слово вокруг пропуска не форма ответа
		for _, w := range lemma.Words(before + " " + after) {
			if matches(w, tc.target) || partMatches(w, tc.target) {
				t.Errorf("%q: в тексте вокруг пропуска осталось %q", tc.sentence, w)
			}
		}
	}
}

// Негодные предложения: цель дважды, внутри составного слова, без цели,
// короче MinWords, длиннее MaxRunes, с разметкой.
func TestFindRejects(t *testing.T) {
	for _, tc := range []struct{ sentence, target string }{
		{"I run fast and she runs faster than me.", "run"},
		{"It is a well-known fact around here.", "know"},
		{"The weather is lovely today, isn't it?", "run"},
		{"Run home now!", "run"},
		{"I run " + strings.Repeat("very ", 40) + "fast.", "run"},
		{"See [the link](https://example.com) to run it.", "run"},
		{"Please run the **second** test again.", "run"},
		{"I run every morning before work.", ""},
	} {
		if b, ok := Find(tc.sentence, tc.target); ok {
			t.Errorf("%q для %q: пропуск %+v, хотели отказ", tc.sentence, tc.target, b)
		}
	}
}

// Ключ пункта зависит от предложения и места пропуска, и только от них.
func TestHash(t *testing.T) {
	s := "I run every morning before work."
	b, _ := Find(s, "run")
	if !bytes.Equal(Hash(s, b), Hash(s, b)) || len(Hash(s, b)) != 32 {
		t.Fatal("ключ не детерминирован или не sha256")
	}
	if bytes.Equal(Hash(s, b), Hash(s, Blank{Start: b.Start + 1, Len: b.Len})) ||
		bytes.Equal(Hash(s, b), Hash(s+" ", b)) {
		t.Fatal("разные пункты с одним ключом")
	}
}

// Ответ засчитывается любой формой слова; exact — именно форма из
// предложения, регистр и пробелы не важны.
func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		answer         string
		correct, exact bool
	}{
		{"ran", true, true},
		{" Ran ", true, true},
		{"run", true, false},
		{"running", true, false},
		{"walked", false, false},
		{"ran!", false, false},
		{"", false, false},
	} {
		correct, exact := Check(tc.answer, "ran", "run")
		if correct != tc.correct || exact != tc.exact {
			t.Errorf("Check(%q) = %v, %v; хотели %v, %v", tc.answer, correct, exact, tc.correct, tc.exact)
		}
	}
}

// Маппинг на FSRS (flows/practice-engine.md): верно и быстро — easy,
// верно — good, неверно — again; порог «быстро» у ввода выше, чем
// у выбора.
func TestRate(t *testing.T) {
	for _, tc := range []struct {
		correct bool
		mode    Mode
		ms      int
		want    srs.Rating
	}{
		{true, Choice, 0, srs.Easy},
		{true, Choice, FastMS(Choice), srs.Easy},
		{true, Choice, FastMS(Choice) + 1, srs.Good},
		{true, Typing, FastMS(Choice) + 1, srs.Easy},
		{true, Typing, FastMS(Typing) + 1, srs.Good},
		{false, Choice, 100, srs.Again},
		{false, Typing, 60000, srs.Again},
	} {
		if got := Rate(tc.correct, tc.mode, tc.ms); got != tc.want {
			t.Errorf("Rate(%v, %s, %d) = %s, хотели %s", tc.correct, tc.mode, tc.ms, got, tc.want)
		}
	}
	if FastMS(Typing) <= FastMS(Choice) {
		t.Fatal("набрать слово быстрее, чем нажать кнопку?")
	}
	for _, s := range []string{"choice", "typing"} {
		if m, ok := ParseMode(s); !ok || string(m) != s {
			t.Errorf("режим %q не разобран", s)
		}
	}
	if _, ok := ParseMode("voice"); ok {
		t.Fatal("неизвестный режим принят")
	}
}
