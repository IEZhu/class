// Package lemma — английская лемматизация (ADR-017): алгоритм morphy
// из WordNet поверх встроенных данных Open English WordNet 2025
// (CC BY 4.0 + лицензия WordNet, тексты — в oewn/LICENSE).
//
// Кандидаты в леммы — само слово, неправильные формы из списков
// исключений и формы, полученные отбрасыванием окончаний по правилам
// своей части речи; кандидат принимается, только если такая лемма
// с такой частью речи есть в словаре. Части речи в тексте мы не знаем,
// поэтому из кандидатов берётся самый многозначный (сумма синсетов по
// всем частям речи — грубая, но доступная замена частотности: OEWN
// не публикует счётчики употреблений), при равенстве — само слово.
// Результат зависит только от слова, а не от контекста: одна и та же
// форма всегда даёт один и тот же термин.
package lemma

import (
	"bufio"
	"compress/gzip"
	"embed"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// Lang — язык лемматизатора и словаря terms (terms.lang): только
// английский.
const Lang = "en"

//go:generate go run ./gen -zip english-wordnet-2025.zip -out oewn

//go:embed oewn/lemmas.tsv.gz oewn/exceptions.tsv.gz
var dataFS embed.FS

type pos uint8

const (
	noun pos = 1 << iota
	verb
	adj
	adv
)

// posOrder — порядок перебора частей речи при сборе кандидатов.
var posOrder = []pos{noun, verb, adj, adv}

var posLetters = map[byte]pos{'n': noun, 'v': verb, 'a': adj, 'r': adv}

// rule — отбросить suffix и дописать repl (morphy(7WN), «detachment rules»).
type rule struct{ suffix, repl string }

var rules = map[pos][]rule{
	noun: {{"s", ""}, {"ses", "s"}, {"xes", "x"}, {"zes", "z"}, {"ches", "ch"},
		{"shes", "sh"}, {"men", "man"}, {"ies", "y"}},
	verb: {{"s", ""}, {"ies", "y"}, {"es", "e"}, {"es", ""}, {"ed", "e"},
		{"ed", ""}, {"ing", "e"}, {"ing", ""}},
	adj: {{"er", ""}, {"est", ""}, {"er", "e"}, {"est", "e"}},
}

// contractions — слова с «'s», где это не притяжательный падеж,
// а сокращённое is/has/us: их не режем, словарь знает их целиком.
var contractions = map[string]bool{
	"it's": true, "he's": true, "she's": true, "that's": true, "what's": true,
	"there's": true, "here's": true, "who's": true, "where's": true,
	"how's": true, "let's": true, "when's": true, "why's": true,
}

type entry struct {
	pos    pos
	senses int
}

type dictionary struct {
	lemmas     map[string]entry
	exceptions map[pos]map[string][]string
}

// load читает встроенные данные один раз. Они попадают в бинарь при
// сборке, и тест пакета разбирает их целиком, поэтому ошибка здесь —
// испорченная сборка, а не состояние среды: паника честнее молчаливого
// «лемма = слово».
var load = sync.OnceValue(func() *dictionary {
	d := &dictionary{lemmas: map[string]entry{}, exceptions: map[pos]map[string][]string{}}
	for _, p := range posOrder {
		d.exceptions[p] = map[string][]string{}
	}
	err := readTSV("oewn/lemmas.tsv.gz", func(f []string) error {
		if len(f) != 3 {
			return fmt.Errorf("строка леммы: %q", f)
		}
		var e entry
		for i := 0; i < len(f[1]); i++ {
			p, ok := posLetters[f[1][i]]
			if !ok {
				return fmt.Errorf("часть речи %q", f[1])
			}
			e.pos |= p
		}
		n, err := strconv.Atoi(f[2])
		if err != nil {
			return err
		}
		e.senses = n
		d.lemmas[f[0]] = e
		return nil
	})
	if err == nil {
		err = readTSV("oewn/exceptions.tsv.gz", func(f []string) error {
			if len(f) != 3 || len(f[0]) != 1 {
				return fmt.Errorf("строка исключения: %q", f)
			}
			p, ok := posLetters[f[0][0]]
			if !ok {
				return fmt.Errorf("часть речи %q", f[0])
			}
			// у формы бывает несколько строк: better → good (adj.exc) и well
			d.exceptions[p][f[1]] = append(d.exceptions[p][f[1]], strings.Fields(f[2])...)
			return nil
		})
	}
	if err != nil {
		panic("lemma: встроенные данные OEWN: " + err.Error())
	}
	return d
})

func readTSV(name string, fn func([]string) error) error {
	f, err := dataFS.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		if err := fn(strings.Split(sc.Text(), "\t")); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return sc.Err()
}

// MaxWordBytes — потолок длины слова: самые длинные слова английского
// словаря короче, а клик и импорт не тратят время на мусор.
const MaxWordBytes = 64

// IsWord — одно слово, которое может стать термином: начинается с буквы,
// дальше буквы, диакритика, внутренние апостроф и дефис; без пробелов
// и цифр. Общее правило для клика и для импорта словаря: статья, которую
// нельзя кликнуть, в словарь не попадает.
func IsWord(s string) bool {
	if s == "" || len(s) > MaxWordBytes {
		return false
	}
	prev := ' '
	for i, r := range s {
		switch {
		case unicode.IsLetter(r):
		case unicode.IsMark(r) && i > 0:
		case (r == '\'' || r == '\u2019' || r == '-') && i > 0 && prev != '\'' && prev != '\u2019' && prev != '-':
		default:
			return false
		}
		prev = r
	}
	// апостроф в конце допустим (students'), дефис — нет
	return prev != '-'
}

// Normalize приводит словоформу к ключу словаря: нижний регистр,
// типографский апостроф → ASCII, без притяжательного 's / s'.
// Пустая строка — во входе нет букв.
func Normalize(surface string) string {
	w := strings.ToLower(strings.TrimSpace(surface))
	w = strings.NewReplacer("\u2019", "'", "\u02bc", "'").Replace(w)
	if !strings.ContainsFunc(w, unicode.IsLetter) {
		return ""
	}
	switch {
	case contractions[w]: // it's = it is: не притяжательный, не режем
	case strings.HasSuffix(w, "'s") && len(w) > 2:
		w = w[:len(w)-2]
	case strings.HasSuffix(w, "s'") && len(w) > 2:
		w = w[:len(w)-1]
	}
	return w
}

// Lemma — лемма словоформы. Слово, которого нет в OEWN (служебные
// слова, имена, опечатки), возвращается нормализованным как есть.
func Lemma(surface string) string {
	w := Normalize(surface)
	if w == "" {
		return ""
	}
	d := load()
	best, bestSenses := w, -1
	consider := func(c string, p pos) {
		e := d.lemmas[c]
		if e.pos&p == 0 {
			return
		}
		if e.senses > bestSenses {
			best, bestSenses = c, e.senses
		}
	}
	consider(w, noun|verb|adj|adv)
	for _, p := range posOrder {
		for _, l := range d.exceptions[p][w] {
			consider(l, p)
		}
		for _, r := range rules[p] {
			base, ok := strings.CutSuffix(w, r.suffix)
			if !ok || base == "" {
				continue
			}
			// Короткая основа CVC перед -ed/-ing/-er/-est удваивает согласную
			// (hop → hopped, big → bigger; такие формы — в исключениях),
			// поэтому hoped и hoping — это hope, а не hop
			if r.repl == "" && doubling[r.suffix] && endsCVC(base) && d.is(base+"e", p) {
				continue
			}
			consider(base+r.repl, p)
		}
	}
	return best
}

func (d *dictionary) is(word string, p pos) bool {
	return d.lemmas[word].pos&p != 0
}

// doubling — окончания, перед которыми английская орфография удваивает
// конечную согласную короткой основы.
var doubling = map[string]bool{"ed": true, "ing": true, "er": true, "est": true}

// endsCVC — основа кончается на «согласная, гласная, согласная» (кроме
// w, x, y, которые не удваиваются): hop, plan, big.
func endsCVC(base string) bool {
	if len(base) < 3 {
		return false
	}
	c1, v, c2 := base[len(base)-3], base[len(base)-2], base[len(base)-1]
	return consonant(c1) && strings.IndexByte("aeiou", v) >= 0 &&
		consonant(c2) && strings.IndexByte("wxy", c2) < 0
}

func consonant(b byte) bool {
	return b >= 'a' && b <= 'z' && strings.IndexByte("aeiou", b) < 0
}

// Known — есть ли слово среди лемм OEWN (любой части речи). По нему
// импорт словаря решает, какие статьи вообще достижимы из Lemma.
func Known(word string) bool {
	_, ok := load().lemmas[word]
	return ok
}
