package lemma

import (
	"strings"
	"unicode"
)

// Words — слова текста: буквы (с диакритикой), внутри — апостроф или
// дефис перед буквой. Цифры и знаки — разделители; апостроф в конце
// (students') в слово не входит, Normalize всё равно бы его срезал.
// Один токенизатор на Go-стороне: валидатор покрытия (S5-6, ADR-030),
// слова доски урока (S3-7, ADR-033) и пропуск cloze (S4-4, ADR-037).
func Words(text string) []string {
	rs := []rune(text)
	spans := WordSpans(text)
	out := make([]string, 0, len(spans))
	for _, sp := range spans {
		out = append(out, string(rs[sp.Start:sp.End]))
	}
	return out
}

// Span — место слова в тексте: руны [Start, End).
type Span struct{ Start, End int }

// WordSpans — те же слова, что у Words, местами в тексте (в рунах):
// cloze вырезает слово из предложения, не теряя знаков вокруг.
func WordSpans(text string) []Span {
	rs := []rune(text)
	var out []Span
	for i := 0; i < len(rs); {
		if !unicode.IsLetter(rs[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(rs) {
			r := rs[j]
			if unicode.IsLetter(r) || unicode.IsMark(r) {
				j++
				continue
			}
			if (r == '\'' || r == '’' || r == '-') && j+1 < len(rs) && unicode.IsLetter(rs[j+1]) {
				j += 2
				continue
			}
			break
		}
		out = append(out, Span{Start: i, End: j})
		i = j
	}
	return out
}

// FunctionWord — служебное ли слово английского (ключ Normalize):
// артикли, местоимения, предлоги, союзы, вспомогательные и модальные
// глаголы, частотные наречия и числительные. В OEWN только
// знаменательные части речи: без этого списка валидатор покрытия
// считал бы the, of и and «незнакомыми», а кандидаты словаря урока
// с доски тонули бы в be, have и a.
func FunctionWord(word string) bool { return functionWords[word] }

var functionWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		a an the this that these those
		i me my mine myself you your yours yourself yourselves he him his himself
		she her hers herself it its itself we us our ours ourselves they them
		their theirs themselves who whom whose which what whatever whoever
		someone somebody something anyone anybody anything everyone everybody
		everything nobody nothing none one ones each every either neither both
		all any some few many much more most less least several such own other
		another same
		about above across after against along among around as at before behind
		below beneath beside besides between beyond by despite down during except
		for from in inside into like near of off on onto out outside over past
		since than through throughout till to toward towards under underneath
		until up upon via with within without
		and but or nor so yet if because although though while whereas unless
		whether once when whenever where wherever why how however therefore
		be am is are was were been being have has had having do does did done
		doing will would shall should can could may might must ought
		can't won't shan't cannot
		not no yes very too also just only even still already again ever never
		always often sometimes usually here there then now today tonight
		yesterday tomorrow quite rather really almost enough
		two three four five six seven eight nine ten first second third
		oh ok okay please`) {
		m[w] = true
	}
	return m
}()
