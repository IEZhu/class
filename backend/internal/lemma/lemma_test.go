package lemma

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// Встроенные данные разбираются целиком и похожи на полный OEWN:
// обрезанный при генерации файл дал бы тихую деградацию, а не ошибку.
func TestEmbeddedData(t *testing.T) {
	d := load()
	if len(d.lemmas) < 70000 {
		t.Fatalf("лемм %d, ждали полный OEWN (>70k)", len(d.lemmas))
	}
	exc := 0
	for _, m := range d.exceptions {
		exc += len(m)
	}
	if exc < 4000 {
		t.Fatalf("неправильных форм %d, ждали >4000", exc)
	}
	if !Known("run") || Known("running's") {
		t.Fatal("Known не сходится со словарём")
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Teacher's":  "teacher",
		"teacher’s":  "teacher",
		"students'":  "students",
		"It's":       "it's",
		"Let’s":      "let's",
		"  Word  ":   "word",
		"well-known": "well-known",
		"'s":         "'s",
		"123":        "",
		"":           "",
		"--":         "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, хотели %q", in, got, want)
		}
	}
}

func TestIsWord(t *testing.T) {
	for in, want := range map[string]bool{
		"run": true, "Running": true, "well-known": true, "don't": true,
		"teacher’s": true, "students'": true, "naïve": true, "café": true,
		"": false, "two words": false, "42": false, "R2D2": false, "-run": false,
		"run-": false, "'run": false, "a--b": false, "e.g.": false,
		strings.Repeat("a", MaxWordBytes): true, strings.Repeat("a", MaxWordBytes+1): false,
	} {
		if got := IsWord(in); got != want {
			t.Errorf("IsWord(%q) = %v, хотели %v", in, got, want)
		}
	}
}

// Слова вне WordNet (служебные, имена, неизвестные) остаются собой:
// лемматизатор не придумывает основу, которой нет в словаре.
func TestLemmaOutsideWordNet(t *testing.T) {
	for in, want := range map[string]string{
		"the":     "the",
		"She":     "she",
		"LiveKit": "livekit",
		"don't":   "don't",
		"It’s":    "it's",
		"news":    "news",
		"data":    "data",
		"":        "",
		"42":      "",
	} {
		if got := Lemma(in); got != want {
			t.Errorf("Lemma(%q) = %q, хотели %q", in, got, want)
		}
	}
}

// Неправильные формы — обратный ход Lemma: каждая сводится к своей
// лемме, better — к good (у него больше синсетов), а не к well.
func TestForms(t *testing.T) {
	for word, want := range map[string]string{
		"run":   "ran running",
		"go":    "goes gone went",
		"child": "children",
		"good":  "best better",
		"well":  "",
		"news":  "",
		"":      "",
	} {
		got := strings.Join(Forms(word), " ")
		if got != want {
			t.Errorf("Forms(%q) = %q, хотели %q", word, got, want)
		}
	}
	for _, form := range Forms("see") {
		if Lemma(form) != "see" {
			t.Errorf("форма %q сводится к %q, а не к see", form, Lemma(form))
		}
	}
	// Результат — копия: правка вызывающего не портит общий индекс
	Forms("run")[0] = "x"
	if Forms("run")[0] != "ran" {
		t.Fatal("Forms отдаёт общий срез")
	}
}

// Замер точности на выборке словоформ (ADR-017): общий порог и полный
// список промахов в выводе теста. Неоднозначные формы (building, tired)
// входят в выборку честно — без части речи их не развести.
func TestLemmaAccuracy(t *testing.T) {
	f, err := os.Open("testdata/inflections.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	total, ok := 0, 0
	groups := map[string][2]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Fatalf("строка выборки: %q", line)
		}
		form, want, group := parts[0], parts[1], parts[2]
		got := Lemma(form)
		g := groups[group]
		g[1]++
		total++
		if got == want {
			ok++
			g[0]++
		} else {
			t.Logf("промах: %s → %s, ожидали %s (%s)", form, got, want, group)
		}
		groups[group] = g
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for name, g := range groups {
		t.Logf("%-15s %d/%d", name, g[0], g[1])
	}
	t.Logf("итого %d/%d = %.1f%%", ok, total, 100*float64(ok)/float64(total))
	if total < 100 {
		t.Fatalf("выборка %d форм, нужно не меньше 100", total)
	}
	if float64(ok)/float64(total) < 0.9 {
		t.Fatalf("точность %d/%d ниже 90%%", ok, total)
	}
}
