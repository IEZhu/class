package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IEZhu/class/backend/internal/lemma"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/translate"
)

const (
	// termLang — язык словаря. Целевой язык перевода — русский: ученики
	// русскоязычные (integrations/translation.md).
	termLang        = "en"
	translationLang = "ru"
	// maxContextRunes — предложение вокруг слова; длиннее — уже абзац
	maxContextRunes = 1000
	// maxDefinitions — статей в попапе: у set и run их десятки
	maxDefinitions = 8
)

type termClickRequest struct {
	Surface string `json:"surface"`
	// ContextSentence пока только проверяется: по нему будут работать
	// «объясни» (S5-8, долг D-2) и кандидаты словаря урока (S3-7)
	ContextSentence string `json:"context_sentence"`
	LessonID        *int64 `json:"lesson_id"`
}

type termTranslation struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

type termDefinition struct {
	Word           string   `json:"word"`
	Pos            string   `json:"pos"`
	Glosses        []string `json:"glosses"`
	TranslationsRU []string `json:"translations_ru"`
}

type termClickResponse struct {
	// TermID — ссылка для следующих шагов попапа: «в мой словарь» (S3-5),
	// «объясни» (S5-8). 0 — слово незнакомо (нет ни в OEWN, ни в датасете):
	// термин не заводится
	TermID  int64  `json:"term_id"`
	Lemma   string `json:"lemma"`
	Surface string `json:"surface"`
	// Translation — null, если перевода нет в кэше и LibreTranslate
	// не настроен или не ответил
	Translation *termTranslation `json:"translation"`
	Definitions []termDefinition `json:"definitions"`
	// Examples — примеры Tatoeba: поле в контракте с S3-2, наполнит S5-4
	// (долг D-1)
	Examples []struct{} `json:"examples"`
}

// handleTermClick — POST /terms/click (S3-2, flows/word-click.md).
// Источники по integrations/translation.md: кэш термина → словарный
// датасет → LibreTranslate при кэш-миссе → запись в кэш. Повторный клик
// по тому же слову (в любой форме) во внешние сервисы не ходит. Термин
// заводится и переводится, только если слово знакомо — его лемма есть
// в OEWN или у него есть статьи датасета (ADR-017): иначе клики по
// произвольным буквам засоряли бы общий словарь и занимали LibreTranslate.
func (a *API) handleTermClick(w http.ResponseWriter, r *http.Request) {
	var req termClickRequest
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "некорректное тело запроса")
		return
	}
	surface := strings.TrimSpace(req.Surface)
	key := lemma.Normalize(surface)
	if !lemma.IsWord(surface) || key == "" {
		writeError(w, http.StatusBadRequest, "bad_request",
			"surface — одно слово из букв (апостроф и дефис внутри), до 64 байт")
		return
	}
	if utf8.RuneCountInString(req.ContextSentence) > maxContextRunes {
		writeError(w, http.StatusBadRequest, "bad_request", "context_sentence длиннее 1000 символов")
		return
	}
	if req.LessonID != nil && !a.checkClickLesson(w, r, *req.LessonID) {
		return
	}

	ctx := r.Context()
	lem := lemma.Lemma(surface)
	keys := []string{lem}
	if key != lem {
		keys = append(keys, key)
	}
	defs, err := a.store.Definitions(ctx, keys, surface, maxDefinitions)
	if err != nil {
		internalError(w, "definitions", err)
		return
	}

	resp := termClickResponse{
		Lemma: lem, Surface: surface,
		Definitions: make([]termDefinition, 0, len(defs)), Examples: []struct{}{},
	}
	for _, d := range defs {
		resp.Definitions = append(resp.Definitions, termDefinition{
			Word: d.Word, Pos: d.Pos, Glosses: d.Glosses, TranslationsRU: d.TranslationsRU,
		})
	}
	if !lemma.Known(lem) && len(defs) == 0 {
		// Незнакомое слово: ответ без термина и перевода, в базу ничего
		writeJSON(w, http.StatusOK, resp)
		return
	}
	term, err := a.store.EnsureTerm(ctx, termLang, lem, surface)
	if err != nil {
		internalError(w, "ensure term", err)
		return
	}
	resp.TermID = term.ID
	if tr, ok := term.Translations[translationLang]; ok {
		resp.Translation = &termTranslation{Text: tr.Text, Source: tr.Source}
	} else {
		resp.Translation = a.translateTerm(ctx, term)
	}
	writeJSON(w, http.StatusOK, resp)
}

// checkClickLesson — клик на уроке: урок существует и виден кликнувшему.
// Сейчас lesson_id ничего не пишет; кандидатов словаря урока заведёт S3-7,
// и контракт к тому времени уже не пропускает чужие уроки.
func (a *API) checkClickLesson(w http.ResponseWriter, r *http.Request, lessonID int64) bool {
	if lessonID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "некорректный lesson_id")
		return false
	}
	d, err := a.store.GetLessonDetail(r.Context(), lessonID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "урок не найден")
		return false
	case err != nil:
		internalError(w, "get lesson", err)
		return false
	case !canSeeLesson(userFrom(r.Context()), d):
		writeError(w, http.StatusForbidden, "forbidden", "нет доступа к этому уроку")
		return false
	}
	return true
}

// translateTerm — перевод леммы через LibreTranslate и запись в кэш.
// Сервис не настроен или не ответил — nil: попап покажет дефиниции без
// перевода, а следующий клик попробует снова (неудача не кэшируется).
func (a *API) translateTerm(ctx context.Context, term *store.Term) *termTranslation {
	if a.translator == nil {
		return nil
	}
	tctx, cancel := context.WithTimeout(ctx, translate.Timeout)
	defer cancel()
	text, err := a.translator.Translate(tctx, term.Lemma, termLang, translationLang)
	if err != nil {
		logf("translate term %d: %v", term.ID, err)
		return nil
	}
	tr := store.Translation{Text: text, Source: "libretranslate", At: time.Now().UTC()}
	// Не записали кэш — перевод всё равно отдаём, следующий клик повторит
	if err := a.store.CacheTranslation(ctx, term.ID, translationLang, tr); err != nil {
		logf("cache translation term %d: %v", term.ID, err)
	}
	return &termTranslation{Text: tr.Text, Source: tr.Source}
}
