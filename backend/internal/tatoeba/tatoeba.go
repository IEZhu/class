// Package tatoeba — корпус примеров Tatoeba для попапа слова (S5-4,
// ADR-028): потоковый импорт поязыковых выгрузок tatoeba.org (подробные
// английские и русские предложения, пары переводов eng-rus) в
// tatoeba_sentences и tatoeba_links. Остаются только английские
// предложения с русским переводом; у английских хранятся леммы слов —
// по ним клик находит примеры термина.
package tatoeba

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IEZhu/class/backend/internal/lemma"
)

// Официальные еженедельные выгрузки (https://tatoeba.org/downloads,
// обновляются по субботам). Подробные — ради автора: CC BY требует его имени.
const (
	DefaultLinks   = "https://downloads.tatoeba.org/exports/per_language/eng/eng-rus_links.tsv.bz2"
	DefaultEnglish = "https://downloads.tatoeba.org/exports/per_language/eng/eng_sentences_detailed.tsv.bz2"
	DefaultRussian = "https://downloads.tatoeba.org/exports/per_language/rus/rus_sentences_detailed.tsv.bz2"
)

// Лицензия текстов Tatoeba: использовать предложение можно, только назвав
// автора (tatoeba.org/terms_of_use). Подпись — в ответе клика.
const (
	License    = "CC BY 2.0 FR"
	LicenseURL = "https://creativecommons.org/licenses/by/2.0/fr/"
)

// SentenceURL — страница предложения на tatoeba.org (язык интерфейса сайт
// выберет сам): ссылка атрибуции.
func SentenceURL(id int64) string {
	return "https://tatoeba.org/sentences/show/" + strconv.FormatInt(id, 10)
}

// Потолки корпуса, если в Options не задано иное (ADR-028).
const (
	// DefaultMaxRunes — предложения длиннее в попап не влезают; в выгрузке
	// таких меньше процента
	DefaultMaxRunes = 150
	// DefaultMaxTranslations — русских переводов на английское предложение:
	// попапу хватает одного, а пар eng-rus в полтора раза больше, чем
	// английских предложений с переводом
	DefaultMaxTranslations = 1
)

// Limits — потолки корпуса (ADR-028): размер таблиц и попапа.
type Limits struct {
	MaxRunes        int
	MaxTranslations int
}

// sentence — строка подробной выгрузки предложений.
type sentence struct {
	ID     int64
	Text   string
	Author string // "" — у предложения нет владельца
}

var errMalformed = errors.New("tatoeba: испорченная строка")

// parseSentence разбирает строку `<lang>_sentences_detailed.tsv`:
// id, язык (ISO 639-3), текст, автор, даты добавления и правки.
// Язык не совпал с ожидаемым — тоже испорченная строка: перепутанный
// файл не должен лечь в корпус под чужим языком.
func parseSentence(line []byte, lang3 string) (sentence, error) {
	f := bytes.Split(line, []byte("\t"))
	if len(f) != 6 || string(f[1]) != lang3 {
		return sentence{}, errMalformed
	}
	id, err := strconv.ParseInt(string(f[0]), 10, 64)
	if err != nil || id <= 0 {
		return sentence{}, errMalformed
	}
	text := strings.TrimSpace(string(f[2]))
	author := string(f[3])
	if author == `\N` { // предложение без владельца
		author = ""
	}
	if text == "" || !validText(text) || !validText(author) {
		return sentence{}, errMalformed
	}
	return sentence{ID: id, Text: text, Author: author}, nil
}

// validText — строку примет колонка text в Postgres: UTF-8 без NUL.
func validText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// pair — пара перевода: английское предложение и русское.
type pair struct{ en, ru int64 }

// parseLink разбирает строку `eng-rus_links.tsv`: id английского
// предложения, id русского.
func parseLink(line []byte) (pair, error) {
	enRaw, ruRaw, ok := bytes.Cut(line, []byte("\t"))
	if !ok {
		return pair{}, errMalformed
	}
	en, err := strconv.ParseInt(string(enRaw), 10, 64)
	if err != nil || en <= 0 {
		return pair{}, errMalformed
	}
	ru, err := strconv.ParseInt(string(ruRaw), 10, 64)
	if err != nil || ru <= 0 {
		return pair{}, errMalformed
	}
	return pair{en: en, ru: ru}, nil
}

// Lemmas — леммы слов предложения (internal/lemma, ADR-017), без
// повторов, по алфавиту. Слово — то, что можно кликнуть (lemma.IsWord):
// буквы с апострофом и дефисом внутри; у составного слова (well-known)
// берутся и целое, и части. Числа и знаки пропускаются. Лемма зависит
// только от слова, поэтому предложение со словоформой клика всегда
// находится по лемме термина.
func Lemmas(text string) []string {
	var out []string
	add := func(w string) {
		if lemma.IsWord(w) {
			if l := lemma.Lemma(w); l != "" && !slices.Contains(out, l) {
				out = append(out, l)
			}
		}
	}
	tokens := strings.FieldsFunc(strings.ReplaceAll(text, "’", "'"), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsDigit(r) && r != '\'' && r != '-'
	})
	for _, tok := range tokens {
		tok = strings.TrimRight(strings.TrimLeft(tok, "'-"), "-")
		// Апостроф в конце — кавычка ('run'), кроме притяжательного students'
		if !strings.HasSuffix(tok, "s'") {
			tok = strings.TrimRight(tok, "'")
		}
		add(tok)
		if strings.Contains(tok, "-") {
			for part := range strings.SplitSeq(tok, "-") {
				add(part)
			}
		}
	}
	slices.Sort(out)
	return out
}
