// Package textgen — учебный текст по целевым словам (S5-6,
// integrations/llm.md): параметры из белых списков, промпт, в котором
// список слов — данные (ADR-029), валидатор покрытия на лемматизаторе
// internal/lemma и один ретрай с перечнем недостающих слов (ADR-030).
package textgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/IEZhu/class/backend/internal/lemma"
)

const (
	// MaxTargets — целевых слов в одном тексте. Каждое нужно дважды,
	// и больше двадцати в короткий текст естественно не уложить.
	MaxTargets = 20
	// MinWords, MaxWords, DefaultWords — потолок длины текста в словах.
	MinWords     = 50
	MaxWords     = 400
	DefaultWords = 150
	// wordsPerTarget — на каждое целевое слово нужно хотя бы столько
	// слов текста: иначе два вхождения каждого не уложить связно, а
	// провальная генерация — это два оплаченных вызова.
	wordsPerTarget = 10
	// DefaultGenre — жанр, если он не задан.
	DefaultGenre = "story"
)

// Levels — уровни CEFR, как у groups.level (ADR-005).
var Levels = []string{"A1", "A2", "B1", "B2", "C1", "C2"}

// genres — белый список жанров и их формулировка в промпте. От клиента
// в промпт попадает только ключ из этого списка.
var genres = map[string]string{
	"story":       "a short story",
	"dialogue":    "a dialogue between two people, each line starting with the speaker's name",
	"article":     "a short news-style article",
	"letter":      "an informal letter to a friend",
	"description": "a description of a place, a person or an everyday situation",
}

// Genres — ключи жанров по алфавиту (для сообщений об ошибке и web).
func Genres() []string {
	keys := make([]string, 0, len(genres))
	for k := range genres {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Target — целевое слово: форма для промпта и лемма — ключ terms.lemma.
type Target struct {
	Word  string
	Lemma string
}

// Params — проверенные параметры одной генерации.
type Params struct {
	Targets  []Target
	Level    string
	Genre    string
	MaxWords int
}

// InputError — параметры не прошли проверку; текст — для ответа 400.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func inputErr(format string, args ...any) error {
	return &InputError{Msg: fmt.Sprintf(format, args...)}
}

// NewParams проверяет параметры запроса на границе доверия. Слово —
// только lemma.IsWord (буквы, внутренние апостроф и дефис): ни пробелов,
// ни знаков, ни переводов строк — фраза-инструкция в список не пройдёт.
// Слова с одной леммой (run, ran) сливаются в одно. Пустой жанр —
// DefaultGenre, нулевая длина — DefaultWords.
func NewParams(words []string, level, genre string, maxWords int) (Params, error) {
	if len(words) == 0 {
		return Params{}, inputErr("words — список целевых слов, от 1 до %d", MaxTargets)
	}
	if len(words) > MaxTargets {
		return Params{}, inputErr("слов больше %d", MaxTargets)
	}
	p := Params{Level: strings.ToUpper(strings.TrimSpace(level)), Genre: strings.TrimSpace(genre), MaxWords: maxWords}
	seen := map[string]bool{}
	for _, w := range words {
		w = strings.TrimSpace(w)
		if !lemma.IsWord(w) {
			return Params{}, inputErr("слово %q: одно слово из букв (апостроф и дефис внутри), до %d байт",
				w, lemma.MaxWordBytes)
		}
		lem := lemma.Lemma(w)
		if seen[lem] {
			continue
		}
		seen[lem] = true
		p.Targets = append(p.Targets, Target{Word: lemma.Normalize(w), Lemma: lem})
	}
	if !slices.Contains(Levels, p.Level) {
		return Params{}, inputErr("level — уровень CEFR: %s", strings.Join(Levels, ", "))
	}
	if p.Genre == "" {
		p.Genre = DefaultGenre
	}
	if _, ok := genres[p.Genre]; !ok {
		return Params{}, inputErr("genre — одно из: %s", strings.Join(Genres(), ", "))
	}
	if p.MaxWords == 0 {
		p.MaxWords = DefaultWords
	}
	if p.MaxWords < MinWords || p.MaxWords > MaxWords {
		return Params{}, inputErr("max_words — от %d до %d", MinWords, MaxWords)
	}
	if need := len(p.Targets) * wordsPerTarget; p.MaxWords < need {
		return Params{}, inputErr("для %d слов нужен текст не короче %d слов (max_words)", len(p.Targets), need)
	}
	return p, nil
}

// Lemmas — леммы целей в порядке запроса (generated_texts.target_lemmas).
func (p Params) Lemmas() []string {
	out := make([]string, len(p.Targets))
	for i, t := range p.Targets {
		out[i] = t.Lemma
	}
	return out
}
