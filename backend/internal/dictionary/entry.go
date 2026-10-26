// Package dictionary — словарный датасет для попапа слова (S3-4, ADR-019):
// потоковый импорт JSONL Wiktextract (kaikki.org, английский Wiktionary)
// в таблицу definitions. Из статьи остаётся только то, что показывает
// попап: заголовок, часть речи, первые значения и русские переводы.
package dictionary

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IEZhu/class/backend/internal/lemma"
)

// Потолки статьи, если в Options не задано иное (ADR-019).
const (
	DefaultMaxGlosses      = 3
	DefaultMaxTranslations = 5
	// maxGlossRunes — длиннее значение в попап не влезет; хвост режется
	// с многоточием
	maxGlossRunes = 300
)

// closedPOS — служебные части речи. В WordNet их нет, Lemma возвращает
// такие слова как есть, и статьи для них берутся без проверки по OEWN.
// Без них у самых частотных слов (the, she, of) не было бы дефиниции.
var closedPOS = map[string]bool{
	"article": true, "det": true, "pron": true, "prep": true, "conj": true,
	"particle": true, "contraction": true, "intj": true, "num": true,
}

// skipSenseTags — значения, которые попапу не нужны: ссылки на другую
// форму (plural of …) и устаревшие или ошибочные употребления.
var skipSenseTags = map[string]bool{
	"form-of": true, "alt-of": true, "obsolete": true, "archaic": true,
	"misspelling": true,
}

// Entry — статья в том виде, в каком она ляжет в definitions.
type Entry struct {
	Word           string
	Lemma          string
	Pos            string
	Etymology      int
	Glosses        []string
	TranslationsRU []string
}

// rawEntry — нужные поля строки Wiktextract; остальное (произношения,
// категории, переводы на другие языки) декодер пропускает.
type rawEntry struct {
	Word            string          `json:"word"`
	LangCode        string          `json:"lang_code"`
	Pos             string          `json:"pos"`
	EtymologyNumber json.RawMessage `json:"etymology_number"`
	Senses          []struct {
		Glosses []string `json:"glosses"`
		Tags    []string `json:"tags"`
	} `json:"senses"`
	Translations []struct {
		Code     string `json:"code"`
		LangCode string `json:"lang_code"`
		Word     string `json:"word"`
	} `json:"translations"`
}

// Limits — потолки статьи (ADR-019): размер таблицы и попапа.
type Limits struct {
	MaxGlosses      int
	MaxTranslations int
}

// parseEntry разбирает строку JSONL. ok=false — статья попапу не нужна
// (другой язык, словосочетание, только ссылки на формы, недостижима из
// лемматизатора); err — строка не JSON.
func parseEntry(line []byte, lim Limits) (e Entry, ok bool, err error) {
	// Английских статей в полной выгрузке около пятой части: строки без
	// метки английского не разбираются вовсе. Метка может встретиться
	// и внутри чужой статьи — тогда решит разбор ниже.
	if !bytes.Contains(line, []byte(`"lang_code": "en"`)) && !bytes.Contains(line, []byte(`"lang_code":"en"`)) {
		return Entry{}, false, nil
	}
	var raw rawEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return Entry{}, false, err
	}
	if raw.LangCode != "en" || raw.Pos == "" || !lemma.IsWord(raw.Word) {
		return Entry{}, false, nil
	}
	key := lemma.Normalize(raw.Word)
	if !closedPOS[raw.Pos] && !lemma.Known(key) {
		return Entry{}, false, nil
	}
	e = Entry{Word: raw.Word, Lemma: key, Pos: raw.Pos, Etymology: etymology(raw.EtymologyNumber)}
	seen := map[string]bool{}
senses:
	for _, s := range raw.Senses {
		if len(e.Glosses) >= lim.MaxGlosses {
			break
		}
		for _, tag := range s.Tags {
			if skipSenseTags[tag] {
				continue senses
			}
		}
		if len(s.Glosses) == 0 {
			continue
		}
		// У подзначения glosses — цепочка от общего к частному: в попап
		// идёт последнее, самое конкретное
		g := clip(strings.TrimSpace(s.Glosses[len(s.Glosses)-1]))
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		e.Glosses = append(e.Glosses, g)
	}
	if len(e.Glosses) == 0 {
		return Entry{}, false, nil
	}
	seen = map[string]bool{}
	for _, t := range raw.Translations {
		if len(e.TranslationsRU) >= lim.MaxTranslations {
			break
		}
		if t.Code != "ru" && t.LangCode != "ru" {
			continue
		}
		w := stripStress(strings.TrimSpace(t.Word))
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		e.TranslationsRU = append(e.TranslationsRU, w)
	}
	return e, true, nil
}

// etymology — номер этимологии омонима: в выгрузках бывает и строкой
// ("2"), и числом. Нет или не число — 0, единственная этимология.
func etymology(raw json.RawMessage) int {
	s := strings.Trim(string(raw), `"`)
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 32767 {
		return 0
	}
	return n
}

// stripStress убирает знаки ударения русского Wiktionary (слова́рь →
// словарь): ученику-носителю они мешают, а ё (отдельная буква или е +
// U+0308) остаётся.
func stripStress(s string) string {
	return strings.NewReplacer("́", "", "̀", "").Replace(s)
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxGlossRunes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:maxGlossRunes-1])) + "…"
}
