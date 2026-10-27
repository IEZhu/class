package textgen

import (
	"strings"

	"github.com/IEZhu/class/backend/internal/lemma"
)

const (
	// MinOccurrences — сколько раз встречается каждое целевое слово
	// (integrations/llm.md).
	MinOccurrences = 2
	// MinCoverage — порог покрытия: доля целей, встреченных не реже
	// MinOccurrences. 1 — каждое слово, как требует integrations/llm.md.
	MinCoverage = 1.0
)

// Report — итог валидатора по одному тексту (ADR-030).
type Report struct {
	// Words — слов в тексте; составное well-known — одно слово
	Words int
	// Occurrences — вхождения по леммам целей, включая нули
	Occurrences map[string]int
	// Missing — леммы целей, встреченных реже MinOccurrences, в порядке целей
	Missing []string
	// Coverage — доля целей, встреченных не реже MinOccurrences
	Coverage float64
	// UnknownShare — доля слов, которых нет в словаре OEWN: имена,
	// опечатки, выдуманные слова. Служебные слова и цели не считаются.
	UnknownShare float64
}

// Passed — текст проходит порог покрытия; иначе нужен ретрай.
func (r Report) Passed() bool { return r.Coverage >= MinCoverage }

// Check — валидатор покрытия: токенизация, лемматизация, счёт вхождений
// целей и доля слов вне словаря.
//
// Слово текста засчитывается цели, если у них общий ключ: нормализованная
// форма или лемма (internal/lemma). Так buildings засчитывается цели
// building, хотя лемма building — build, а ran — цели run. Части
// составного слова тоже ключи: well-known засчитывается и well, и known.
func Check(text string, targets []Target) Report {
	rep := Report{Occurrences: make(map[string]int, len(targets))}
	targetKeys := make([]map[string]bool, len(targets))
	for i, t := range targets {
		targetKeys[i] = map[string]bool{t.Lemma: true, t.Word: true}
		rep.Occurrences[t.Lemma] = 0
	}
	unknown := 0
	for _, tok := range lemma.Words(text) {
		rep.Words++
		ks := keys(tok)
		matched := false
		for i, t := range targets {
			if hasAny(targetKeys[i], ks) {
				rep.Occurrences[t.Lemma]++
				matched = true
			}
		}
		if !matched && !known(tok) {
			unknown++
		}
	}
	covered := 0
	for _, t := range targets {
		if rep.Occurrences[t.Lemma] >= MinOccurrences {
			covered++
		} else {
			rep.Missing = append(rep.Missing, t.Lemma)
		}
	}
	if len(targets) > 0 {
		rep.Coverage = float64(covered) / float64(len(targets))
	}
	if rep.Words > 0 {
		rep.UnknownShare = float64(unknown) / float64(rep.Words)
	}
	return rep
}

// keys — ключи слова для сравнения с целями: нормализованная форма,
// лемма и то же для частей составного слова.
func keys(tok string) []string {
	n := lemma.Normalize(tok)
	ks := []string{n, lemma.Lemma(tok)}
	if strings.Contains(n, "-") {
		for _, part := range strings.Split(n, "-") {
			ks = append(ks, part, lemma.Lemma(part))
		}
	}
	return ks
}

func hasAny(set map[string]bool, ks []string) bool {
	for _, k := range ks {
		if set[k] {
			return true
		}
	}
	return false
}

// clitics — сокращённые хвосты: they're, we'll, I've, she'd, I'm, don't,
// it's (притяжательный 's срезает ещё Normalize, сюда доходят только
// сокращения is/has/us). Слово с таким хвостом известно, если известна
// основа.
var clitics = []string{"n't", "'re", "'ll", "'ve", "'d", "'m", "'s"}

// known — слово есть в OEWN (формой или леммой) или оно служебное.
// Составное известно, если известна каждая часть.
func known(tok string) bool {
	n := lemma.Normalize(tok)
	if lemma.FunctionWord(n) || lemma.Known(n) || lemma.Known(lemma.Lemma(tok)) {
		return true
	}
	for _, c := range clitics {
		if base, ok := strings.CutSuffix(n, c); ok && base != "" {
			return lemma.FunctionWord(base) || lemma.Known(lemma.Lemma(base))
		}
	}
	if strings.Contains(n, "-") {
		for _, part := range strings.Split(n, "-") {
			if !lemma.FunctionWord(part) && !lemma.Known(lemma.Lemma(part)) {
				return false
			}
		}
		return true
	}
	return false
}
