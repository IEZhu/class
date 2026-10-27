// Package lessonterms — кандидаты словаря урока из текста (S3-7, ADR-033):
// слова текстовых элементов доски и ручной ввод преподавателя приводятся
// к терминам по правилу клика по слову (ADR-017): лемма — internal/lemma,
// термин заводится только у знакомого слова.
package lessonterms

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"

	"github.com/IEZhu/class/backend/internal/lemma"
	"github.com/IEZhu/class/backend/internal/store"
)

// MaxWhiteboardTerms — потолок кандидатов с одной доски: самые частые
// слова. Больше преподаватель в чеклисте не разберёт, а вставленная
// на доску статья не заводит тысячи терминов за раз.
const MaxWhiteboardTerms = 500

// Word — слово текста, сведённое к лемме: форма первой встречи и число
// вхождений всех форм.
type Word struct {
	Lemma   string
	Surface string
	Count   int
}

// sceneElement — поля элемента Excalidraw, нужные разбору (тип
// ExcalidrawTextElement пакета @excalidraw/excalidraw 0.18).
type sceneElement struct {
	Type      string `json:"type"`
	IsDeleted bool   `json:"isDeleted"`
	Text      string `json:"text"`
	// OriginalText — текст до переноса по ширине: перенос вставляет
	// в text переводы строк и рвёт длинное слово посередине
	OriginalText string `json:"originalText"`
}

// SceneTexts — тексты живых текстовых элементов сцены (whiteboards.scene —
// массив элементов как есть, с удалёнными), в том числе подписей внутри
// фигур: они тоже type "text", с containerId.
func SceneTexts(scene json.RawMessage) ([]string, error) {
	var elements []sceneElement
	if err := json.Unmarshal(scene, &elements); err != nil {
		return nil, fmt.Errorf("сцена доски: %w", err)
	}
	var out []string
	for _, e := range elements {
		if e.Type != "text" || e.IsDeleted {
			continue
		}
		text := e.OriginalText
		if text == "" {
			text = e.Text
		}
		if text != "" {
			out = append(out, text)
		}
	}
	return out, nil
}

// TextWords — слова текстов по леммам: токенизатор lemma.Words, служебные
// слова (lemma.FunctionWord) пропускаются — на доске они есть в каждой
// фразе и в словарь урока не просятся. Порядок — по числу вхождений,
// при равенстве — по лемме.
func TextWords(texts []string) []Word {
	byLemma := map[string]*Word{}
	for _, text := range texts {
		for _, tok := range lemma.Words(text) {
			if !lemma.IsWord(tok) || lemma.FunctionWord(lemma.Normalize(tok)) {
				continue
			}
			lem := lemma.Lemma(tok)
			if lem == "" {
				continue
			}
			if w, ok := byLemma[lem]; ok {
				w.Count++
				continue
			}
			byLemma[lem] = &Word{Lemma: lem, Surface: tok, Count: 1}
		}
	}
	out := make([]Word, 0, len(byLemma))
	for _, w := range byLemma {
		out = append(out, *w)
	}
	slices.SortFunc(out, func(a, b Word) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Lemma, b.Lemma))
	})
	return out
}

// Known делит слова на знакомые и нет (ADR-017: лемма есть в OEWN или
// у неё есть статьи словарного датасета) — одним запросом к датасету
// на все леммы вне OEWN. Порядок слов сохраняется.
func Known(ctx context.Context, st *store.Store, words []Word) (known, unknown []Word, err error) {
	var ask []string
	for _, w := range words {
		if !lemma.Known(w.Lemma) {
			ask = append(ask, w.Lemma)
		}
	}
	defined, err := st.DefinedLemmas(ctx, ask)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range words {
		if lemma.Known(w.Lemma) || defined[w.Lemma] {
			known = append(known, w)
		} else {
			unknown = append(unknown, w)
		}
	}
	return known, unknown, nil
}

// Terms заводит термины знакомых слов, как первый клик (store.EnsureTerm):
// форма первой встречи становится surface нового термина.
func Terms(ctx context.Context, st *store.Store, known []Word) ([]store.CandidateTerm, error) {
	out := make([]store.CandidateTerm, 0, len(known))
	for _, w := range known {
		term, err := st.EnsureTerm(ctx, lemma.Lang, w.Lemma, w.Surface)
		if err != nil {
			return nil, err
		}
		out = append(out, store.CandidateTerm{TermID: term.ID, Surface: w.Surface, Count: w.Count})
	}
	return out, nil
}

// CollectWhiteboard — разбор сцены доски урока в кандидатов
// source='whiteboard': по концу урока (джоба whiteboard_terms) и по кнопке
// преподавателя. Повтор над той же сценой ничего не меняет. Возвращает
// число кандидатов доски; доски нет — 0.
func CollectWhiteboard(ctx context.Context, st *store.Store, lessonID int64) (int, error) {
	scene, err := st.WhiteboardScene(ctx, lessonID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	texts, err := SceneTexts(scene)
	if err != nil {
		return 0, fmt.Errorf("урок %d: %w", lessonID, err)
	}
	known, _, err := Known(ctx, st, TextWords(texts))
	if err != nil {
		return 0, err
	}
	if len(known) > MaxWhiteboardTerms {
		log.Printf("lessonterms: урок %d: на доске %d знакомых слов, в кандидаты — %d самых частых",
			lessonID, len(known), MaxWhiteboardTerms)
		known = known[:MaxWhiteboardTerms]
	}
	terms, err := Terms(ctx, st, known)
	if err != nil {
		return 0, err
	}
	return len(terms), st.SaveWhiteboardCandidates(ctx, lessonID, terms)
}
