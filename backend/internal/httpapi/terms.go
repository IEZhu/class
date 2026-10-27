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
	"github.com/IEZhu/class/backend/internal/tatoeba"
	"github.com/IEZhu/class/backend/internal/translate"
)

const (
	// termLang — язык словаря. Целевой язык перевода — русский: ученики
	// русскоязычные (integrations/translation.md).
	termLang        = lemma.Lang
	translationLang = "ru"
	// maxContextRunes — предложение вокруг слова; длиннее — уже абзац
	maxContextRunes = 1000
	// maxDefinitions — статей в попапе: у set и run их десятки
	maxDefinitions = 8
	// maxExamples — примеров Tatoeba в попапе (flows/word-click.md)
	maxExamples = 2
)

type termClickRequest struct {
	Surface string `json:"surface"`
	// ContextSentence пока только проверяется: попап шлёт его же в
	// «объясни» (S5-8)
	ContextSentence string `json:"context_sentence"`
	// LessonID — клик на странице урока: у участника он ещё и кандидат
	// словаря урока (S3-7, ADR-033)
	LessonID *int64 `json:"lesson_id"`
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
	// «объясни» (S5-8, POST /terms/{id}/explain). 0 — слово незнакомо
	// (нет ни в OEWN, ни в датасете): термин не заводится
	TermID  int64  `json:"term_id"`
	Lemma   string `json:"lemma"`
	Surface string `json:"surface"`
	// Translation — null, если перевода нет в кэше и LibreTranslate
	// не настроен или не ответил
	Translation *termTranslation `json:"translation"`
	Definitions []termDefinition `json:"definitions"`
	// Examples — примеры Tatoeba (S5-4, ADR-028); пустой список, если
	// корпус не импортирован или в нём нет леммы
	Examples []termExample `json:"examples"`
	// Status — статус слова в личном словаре студента (S3-5); null — слова
	// в словаре нет или словаря у роли нет
	Status *int `json:"status"`
	// ExplainAvailable — попап показывает «объясни» (S5-8): у слова есть
	// термин, LLM настроен, роль допускает (canExplain)
	ExplainAvailable bool `json:"explain_available"`
}

// termExample — английское предложение с леммой и его русский перевод.
type termExample struct {
	Text        string             `json:"text"`
	Translation string             `json:"translation"`
	Source      string             `json:"source"`
	Attribution exampleAttribution `json:"attribution"`
}

// exampleAttribution — подпись примера: CC BY 2.0 FR разрешает показ,
// только назвав автора, поэтому у обоих предложений — автор и страница
// на tatoeba.org. Пустой автор — у предложения нет владельца.
type exampleAttribution struct {
	Author            string `json:"author"`
	URL               string `json:"url"`
	TranslationAuthor string `json:"translation_author"`
	TranslationURL    string `json:"translation_url"`
	License           string `json:"license"`
	LicenseURL        string `json:"license_url"`
}

// handleTermClick — POST /terms/click (S3-2, flows/word-click.md).
// Источники по integrations/translation.md: кэш термина → словарный
// датасет → LibreTranslate при кэш-миссе → запись в кэш; примеры — из
// локального корпуса Tatoeba (S5-4). Повторный клик по тому же слову
// (в любой форме) во внешние сервисы не ходит. Термин заводится,
// переводится и получает примеры и статус личного словаря, только если
// слово знакомо — его лемма есть в OEWN или у него есть статьи датасета
// (ADR-017): иначе клики по произвольным буквам засоряли бы общий
// словарь и занимали LibreTranslate.
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
		Definitions: make([]termDefinition, 0, len(defs)), Examples: []termExample{},
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
	if req.LessonID != nil {
		a.recordClickCandidate(ctx, *req.LessonID, term.ID, surface)
	}
	resp.ExplainAvailable = a.canExplain(userFrom(ctx))
	resp.Examples = a.examples(ctx, term)
	if tr, ok := term.Translations[translationLang]; ok {
		resp.Translation = &termTranslation{Text: tr.Text, Source: tr.Source}
	} else {
		resp.Translation = a.translateTerm(ctx, term)
	}
	if u := userFrom(ctx); u.Role == store.RoleStudent {
		if resp.Status, err = a.store.UserTermStatus(ctx, u.ID, term.ID); err != nil {
			internalError(w, "user term status", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// checkClickLesson — клик на уроке: урок существует и виден кликнувшему,
// поэтому кандидата в чужой урок не записать. Та же проверка стережёт
// source_lesson_id личного словаря (S3-5) и предложение урока у «объясни»
// (S5-8).
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

// recordClickCandidate — клик на уроке становится кандидатом словаря урока
// source='click' (S3-7, ADR-033); store засчитывает только клик участника
// из снапшота урока в окне комнаты, раз на человека и в пределах потолка
// кандидатов урока; клики преподавателя и админа — нет. Сбой записи
// попап не ломает: кандидат — подсказка преподавателю, а ответ клика
// нужен ученику сейчас.
func (a *API) recordClickCandidate(ctx context.Context, lessonID, termID int64, surface string) {
	if _, err := a.store.RecordClickCandidate(ctx, lessonID, userFrom(ctx).ID, termID, surface); err != nil {
		logf("click candidate lesson %d term %d: %v", lessonID, termID, err)
	}
}

// examples — 1–2 коротких примера с леммой термина из локального корпуса
// Tatoeba: во внешнюю сеть клик за ними не ходит. Сбой запроса (например,
// миграция 0016 не применена) — пустой список: перевод и дефиниции попап
// покажет и без примеров.
func (a *API) examples(ctx context.Context, term *store.Term) []termExample {
	found, err := a.store.Examples(ctx, term.Lemma, maxExamples)
	if err != nil {
		logf("examples term %d: %v", term.ID, err)
		return []termExample{}
	}
	out := make([]termExample, 0, len(found))
	for _, e := range found {
		out = append(out, termExample{
			Text: e.Text, Translation: e.Translation, Source: "tatoeba",
			Attribution: exampleAttribution{
				Author: e.Author, URL: tatoeba.SentenceURL(e.ID),
				TranslationAuthor: e.TranslationAuthor, TranslationURL: tatoeba.SentenceURL(e.TranslationID),
				License: tatoeba.License, LicenseURL: tatoeba.LicenseURL,
			},
		})
	}
	return out
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
