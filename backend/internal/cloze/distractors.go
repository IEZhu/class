package cloze

import (
	"hash/fnv"
	"slices"
	"strings"

	"github.com/IEZhu/class/backend/internal/lemma"
)

// Target — слово пункта: лемма термина, вырезанная форма и урок, с
// которого слово пришло в словарь (nil — вне урока).
type Target struct {
	Lemma    string
	Surface  string
	LessonID *int64
}

// Word — слово словаря студента, кандидат в дистракторы.
type Word struct {
	Lemma    string
	LessonID *int64
}

func (w Word) sameLesson(t Target) bool {
	return w.LessonID != nil && t.LessonID != nil && *w.LessonID == *t.LessonID
}

// Ступени подбора дистракторов: слова словаря студента той же части
// речи, затем частые слова той же части речи, затем частые любой.
const (
	tierListSamePOS = iota
	tierListSharedPOS
	tierFrequentSamePOS
	tierFrequentSharedPOS
	tierFrequentAny
)

// PickDistractors — Distractors неверных вариантов для пункта (ADR-037):
// слова словаря студента той же части речи по OEWN (точный набор частей
// речи, затем пересекающийся; внутри — слова того же урока первыми),
// затем частые слова той же части речи и, на крайний случай, любой.
// Часть речи цели неизвестна (нет в OEWN) — словарь целиком, затем
// частые слова. Не годятся цель и её формы, служебные слова и слова
// самого предложения. Порядок внутри ступени задаёт seed (ключ пункта),
// поэтому выбор детерминирован. Варианты — леммы: в предложении стоит
// форма, а проверка ответа — по лемме (Check). Ключ цели не из одних
// латинских букв (t-shirt, café, o'clock) — первым берётся такой же
// непохожий дистрактор: иначе ответ был бы единственной кнопкой с
// дефисом, диакритикой или апострофом. Такого слова нет — вариантов нет
// (nil), и пункта из этого предложения не будет (ADR-037). У обычного
// ключа непохожих дистракторов ноль или два: ни одна непохожая кнопка,
// ни одна обычная среди трёх непохожих ответа не выдаёт.
func PickDistractors(t Target, pool []Word, sentence string, seed []byte) []string {
	taken := map[string]bool{t.Lemma: true, lemma.Normalize(t.Surface): true}
	for _, w := range lemma.Words(sentence) {
		taken[lemma.Normalize(w)], taken[lemma.Lemma(w)] = true, true
	}
	pos := lemma.POS(t.Lemma)
	type candidate struct {
		word  string
		tier  int
		other bool // не того урока: в ступени словаря идёт после своего
		order uint64
	}
	var cands []candidate
	add := func(word string, tier int, other bool) {
		cands = append(cands, candidate{word: word, tier: tier, other: other, order: mix(seed, word)})
	}
	for _, w := range pool {
		switch wp := lemma.POS(w.Lemma); {
		case pos == "", wp == pos:
			add(w.Lemma, tierListSamePOS, !w.sameLesson(t))
		case sharesPOS(wp, pos):
			add(w.Lemma, tierListSharedPOS, !w.sameLesson(t))
		}
	}
	for _, w := range frequentWords {
		switch wp := lemma.POS(w); {
		case pos == "", wp == pos:
			add(w, tierFrequentSamePOS, false)
		case sharesPOS(wp, pos):
			add(w, tierFrequentSharedPOS, false)
		default:
			add(w, tierFrequentAny, false)
		}
	}
	slices.SortFunc(cands, func(a, b candidate) int {
		switch {
		case a.tier != b.tier:
			return a.tier - b.tier
		case a.other != b.other:
			if a.other {
				return 1
			}
			return -1
		case a.order != b.order:
			if a.order < b.order {
				return -1
			}
			return 1
		}
		return strings.Compare(a.word, b.word)
	})
	out := make([]string, 0, Distractors)
	// take добирает out до n слов по порядку кандидатов, годных fits
	// (nil — любых)
	take := func(n int, fits func(string) bool) {
		for _, c := range cands {
			if len(out) == n {
				return
			}
			l := lemma.Lemma(c.word)
			if fits != nil && !fits(c.word) || !distractor(c.word) || taken[c.word] || taken[l] {
				continue
			}
			taken[c.word], taken[l] = true, true
			out = append(out, c.word)
		}
	}
	plain := letters(t.Lemma)
	if !plain {
		if take(1, odd); len(out) == 0 {
			return nil
		}
	}
	take(Distractors, nil)
	if plain && countFunc(out, odd)%2 == 1 {
		// Нечётное число непохожих у обычного ключа выдало бы ответ: одна
		// непохожая кнопка — заведомо не он, единственная обычная среди
		// трёх непохожих — он. Последний непохожий уступает место
		// следующему обычному кандидату
		i := len(out) - 1
		for !odd(out[i]) {
			i--
		}
		out = slices.Delete(out, i, i+1)
		take(Distractors, letters)
	}
	return out
}

// odd — слово не из одних строчных латинских букв (дефис, диакритика,
// апостроф): среди ключей словаря такие редки.
func odd(w string) bool { return !letters(w) }

func countFunc(words []string, f func(string) bool) int {
	n := 0
	for _, w := range words {
		if f(w) {
			n++
		}
	}
	return n
}

// distractor — слово годится в варианты: одно слово, не служебное.
func distractor(w string) bool {
	return lemma.IsWord(w) && !lemma.FunctionWord(lemma.Normalize(w))
}

func sharesPOS(a, b string) bool {
	return strings.ContainsAny(a, b) && a != "" && b != ""
}

// mix — порядок слова в ступени: хеш ключа пункта и слова.
func mix(seed []byte, word string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(seed)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(word))
	return h.Sum64()
}

// Options — варианты пункта: ответ и дистракторы в нижнем регистре,
// перемешанные по seed (ключ пункта). Место ответа меняется от пункта
// к пункту, но у одного пункта стабильно — повторная выдача не
// перетасовывает кнопки.
func Options(answer string, distractors []string, seed []byte) []string {
	out := make([]string, 0, len(distractors)+1)
	out = append(out, strings.ToLower(answer))
	for _, d := range distractors {
		out = append(out, strings.ToLower(d))
	}
	slices.SortFunc(out, func(a, b string) int {
		ha, hb := mix(seed, a), mix(seed, b)
		switch {
		case ha < hb:
			return -1
		case ha > hb:
			return 1
		}
		return strings.Compare(a, b)
	})
	return out
}

// frequentWords — запас дистракторов, когда в словаре студента мало слов
// нужной части речи: частые слова общего английского, отобранные вручную
// (не частотный список по корпусу), — существительные, глаголы,
// прилагательные и наречия. Часть речи каждого берётся из OEWN, как
// у слов словаря.
var frequentWords = strings.Fields(`
	time year day week morning night home school house room door window
	city country street car train book letter friend family child teacher
	doctor student water money food music game story picture idea question
	problem answer job office shop garden river table
	go come take make give find tell ask think know see look want use
	work call try leave feel bring keep start show hear play run move live
	believe hold write sit stand lose pay meet learn change watch follow
	stop read open walk win buy wait send build stay fall cut
	good new old long great little big high small large young important bad
	early late hard easy strong free full clear short happy cold hot dark
	quick slow quiet busy cheap expensive
	quickly slowly soon finally suddenly quietly easily carefully clearly
	loudly happily gently safely proudly rarely recently warmly
`)
