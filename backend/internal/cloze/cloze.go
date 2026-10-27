// Package cloze — упражнение «вставь слово» (S4-4, S4-5, S5-8 часть 2;
// ADR-037): предложение с целевым словом из текстов студента, пропуск на
// месте формы этого слова, дистракторы из словаря студента той же части
// речи (иначе частые слова), LLM-дистракторы как данные JSON и правило,
// по которому ответ становится оценкой FSRS. Пакет без SQL и без часов,
// выбор детерминирован: то же предложение и тот же словарь дают тот же
// пропуск, те же варианты и тот же их порядок.
package cloze

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IEZhu/class/backend/internal/lemma"
	"github.com/IEZhu/class/backend/internal/srs"
)

const (
	// Distractors — неверных вариантов у пункта: с ответом — четыре
	// кнопки под клавиши 1–4.
	Distractors = 3
	// Границы предложения в словах и символах: короче — нет контекста,
	// длиннее — пункт не прочесть за секунды, и «быстро» теряет смысл.
	MinWords = 4
	MaxWords = 30
	MaxRunes = 200
)

// markup — знаки разметки и кода: предложение с ними — кусок markdown
// материала или ссылки, а не текст для чтения.
const markup = "*_#[]|`<>{}\\=@/"

// Sentences — предложения текста: строки, без маркеров списков и
// заголовков markdown (материалы урока), разрезанные по . ! ? … перед
// пробелом; пробелы схлопнуты. Сокращения (Mr.) режут предложение —
// обрывок короче MinWords отсеет Find.
func Sentences(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		for _, s := range splitLine(trimMarker(line)) {
			if s = strings.Join(strings.Fields(s), " "); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// trimMarker снимает с начала строки маркеры markdown: «# », «- », «* »,
// «+ », «1. », «1) », «> » (и их повторы: «> - »).
func trimMarker(line string) string {
	for {
		s := strings.TrimLeftFunc(line, unicode.IsSpace)
		rest := s
		switch {
		case strings.HasPrefix(s, "#"):
			rest = strings.TrimLeft(s, "#")
		case strings.HasPrefix(s, ">"):
			rest = s[1:]
		case strings.HasPrefix(s, "- "), strings.HasPrefix(s, "* "), strings.HasPrefix(s, "+ "):
			rest = s[2:]
		default:
			digits := len(s) - len(strings.TrimLeft(s, "0123456789"))
			if digits > 0 && digits <= 3 && len(s) > digits+1 && (s[digits] == '.' || s[digits] == ')') && s[digits+1] == ' ' {
				rest = s[digits+2:]
			}
		}
		if rest == s {
			return s
		}
		line = rest
	}
}

// splitLine режет строку после . ! ? … (и закрывающих кавычек и скобок
// за ними), если дальше пробел или конец строки.
func splitLine(line string) []string {
	rs := []rune(line)
	var out []string
	start := 0
	for i := 0; i < len(rs); i++ {
		if !strings.ContainsRune(".!?…", rs[i]) {
			continue
		}
		j := i + 1
		for j < len(rs) && strings.ContainsRune(".!?…\"'”’)»", rs[j]) {
			j++
		}
		if j == len(rs) || unicode.IsSpace(rs[j]) {
			out = append(out, string(rs[start:j]))
			start, i = j, j
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// Blank — пропуск: руны [Start, Start+Len) предложения.
type Blank struct {
	Start, Len int
}

// Find — пропуск для целевой леммы в предложении: слово, чья форма или
// лемма — цель (ran для run). false — предложение не годится: не по
// размеру, с разметкой, без цели, с целью дважды или внутри составного
// слова (well-known для known) — второе вхождение осталось бы в тексте
// подсказкой. Притяжательное 's остаётся в тексте: пропуск — само слово.
func Find(sentence, target string) (Blank, bool) {
	if target == "" || utf8.RuneCountInString(sentence) > MaxRunes || strings.ContainsAny(sentence, markup) {
		return Blank{}, false
	}
	rs := []rune(sentence)
	spans := lemma.WordSpans(sentence)
	if len(spans) < MinWords || len(spans) > MaxWords {
		return Blank{}, false
	}
	found := -1
	for i, sp := range spans {
		tok := string(rs[sp.Start:sp.End])
		switch {
		case matches(tok, target):
			if found >= 0 {
				return Blank{}, false
			}
			found = i
		case partMatches(tok, target):
			return Blank{}, false
		}
	}
	if found < 0 {
		return Blank{}, false
	}
	sp := spans[found]
	tok := string(rs[sp.Start:sp.End])
	low := strings.ToLower(strings.ReplaceAll(tok, "’", "'"))
	if base, ok := strings.CutSuffix(low, "'s"); ok && lemma.Normalize(tok) == base {
		sp.End -= 2
	}
	return Blank{Start: sp.Start, Len: sp.End - sp.Start}, true
}

// matches — слово текста — форма цели: тот же ключ словаря или лемма.
func matches(tok, target string) bool {
	return lemma.Normalize(tok) == target || lemma.Lemma(tok) == target
}

// partMatches — цель — часть составного слова через дефис.
func partMatches(tok, target string) bool {
	n := lemma.Normalize(tok)
	if !strings.Contains(n, "-") {
		return false
	}
	for _, part := range strings.Split(n, "-") {
		if part == target || lemma.Lemma(part) == target {
			return true
		}
	}
	return false
}

// Parts — предложение вокруг пропуска и сама вырезанная форма.
func Parts(sentence string, b Blank) (before, surface, after string) {
	rs := []rune(sentence)
	return string(rs[:b.Start]), string(rs[b.Start : b.Start+b.Len]), string(rs[b.Start+b.Len:])
}

// Valid — пропуск лежит внутри предложения (строка из базы, ADR-037).
func (b Blank) Valid(sentence string) bool {
	return b.Start >= 0 && b.Len > 0 && b.Start+b.Len <= utf8.RuneCountInString(sentence)
}

// Hash — ключ пункта: sha256 предложения и места пропуска. Один ключ —
// один пункт у пользователя и одна запись кэша LLM-дистракторов слова.
func Hash(sentence string, b Blank) []byte {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d:%d:%s", b.Start, b.Len, sentence))
	return sum[:]
}

// Check — ответ на пункт: correct — сам ключ термина или любая форма
// слова (cloze проверяет слово словаря, а не грамматику: термин — лемма),
// exact — именно форма из предложения. Ключ сверяется сам по себе, как
// в matches: Lemma не идемпотентна на ключах (клик по roses заводит
// термин rose, а Lemma(rose) — rise), а кнопка ответа из словаря — сам
// ключ.
func Check(answer, surface, target string) (correct, exact bool) {
	a := strings.TrimSpace(answer)
	exact = a != "" && lemma.Normalize(a) == lemma.Normalize(surface)
	return exact || lemma.IsWord(a) && matches(a, target), exact
}

// Mode — как отвечали: выбором варианта или вводом с клавиатуры.
type Mode string

const (
	Choice Mode = "choice"
	Typing Mode = "typing"
)

// ParseMode — режим из запроса; false — такого нет.
func ParseMode(s string) (Mode, bool) {
	m := Mode(s)
	_, ok := fastMS[m]
	return m, ok
}

// fastMS — порог «быстро» по режиму, мс: верный ответ быстрее — easy.
// Набрать слово дольше, чем нажать кнопку. Стартовые значения: подобрать
// по журналу cloze_attempts на реальных попытках (ADR-037).
var fastMS = map[Mode]int{Choice: 5000, Typing: 8000}

// FastMS — порог «быстро» режима m в миллисекундах.
func FastMS(m Mode) int { return fastMS[m] }

// Rate — оценка FSRS за ответ (flows/practice-engine.md): верно и
// быстро — easy, верно — good, неверно — again. hard cloze не ставит.
func Rate(correct bool, m Mode, responseMS int) srs.Rating {
	switch {
	case !correct:
		return srs.Again
	case responseMS <= fastMS[m]:
		return srs.Easy
	default:
		return srs.Good
	}
}
