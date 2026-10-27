package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IEZhu/class/backend/internal/explain"
	"github.com/IEZhu/class/backend/internal/lemma"
	"github.com/IEZhu/class/backend/internal/llm"
	"github.com/IEZhu/class/backend/internal/store"
)

const (
	// DefaultLLMExplainDailyLimit — объяснений слов на пользователя в
	// сутки, если LLM_EXPLAIN_DAILY_LIMIT не задан: объяснение — около
	// десятой доли генерации текста по цене (смета — ADR-039). Повтор из
	// кэша лимит не тратит.
	DefaultLLMExplainDailyLimit = 30
	// explainBudget — потолок объяснения: один вызов провайдера и запас
	// на базу.
	explainBudget = llm.Timeout + 5*time.Second
)

type explainRequest struct {
	// Surface и ContextSentence — те же, что ушли в POST /terms/click
	Surface         string `json:"surface"`
	ContextSentence string `json:"context_sentence"`
	LessonID        *int64 `json:"lesson_id"`
}

type explainResponse struct {
	TermID int64  `json:"term_id"`
	Lemma  string `json:"lemma"`
	// Explanation — простой текст, абзацы через пустую строку
	Explanation string `json:"explanation"`
	// Cached — ответ из кэша, провайдер не вызывался
	Cached    bool      `json:"cached"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
}

func toExplainResponse(term *store.Term, e *store.Explanation, cached bool) explainResponse {
	return explainResponse{
		TermID: term.ID, Lemma: term.Lemma, Explanation: e.Body, Cached: cached,
		Provider: e.Provider, Model: e.Model, CreatedAt: e.CreatedAt,
	}
}

// explainRoles — кому доступно «объясни». Вызов стоит денег, поэтому
// гостю события его нет: учётка гостя заводится на каждый вход по
// публичной ссылке, и суточный лимит на пользователя его не держит
// (ADR-039, долг D-27).
var explainRoles = []string{store.RoleStudent, store.RoleTeacher, store.RoleAdmin}

// canExplain — показывать ли в попапе «объясни» (explain_available
// ответа клика): LLM настроен и роль вызывающего его допускает.
func (a *API) canExplain(u *store.User) bool {
	return a.llm != nil && slices.Contains(explainRoles, u.Role)
}

// handleExplainTerm — POST /terms/{id}/explain (S5-8, ADR-039): LLM
// объясняет по-русски значение слова в предложении и его грамматику.
// Предложение — то, в котором слово кликнули; с lesson_id — урок должен
// быть виден вызывающему, как у клика. Ответ кэшируется по термину и
// нормализованному предложению для всех: повторный клик провайдера не
// зовёт и лимит не тратит. Без ключа LLM — 503; место в суточном лимите
// объяснений занимается до вызова и при сбое не возвращается (как у
// генерации, ADR-029).
func (a *API) handleExplainTerm(w http.ResponseWriter, r *http.Request) {
	if a.llm == nil {
		writeError(w, http.StatusServiceUnavailable, "llm_not_configured", "LLM не настроен")
		return
	}
	termID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req explainRequest
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "некорректное тело запроса")
		return
	}
	surface := strings.TrimSpace(req.Surface)
	if !lemma.IsWord(surface) {
		writeError(w, http.StatusBadRequest, "bad_request",
			"surface — одно слово из букв (апостроф и дефис внутри), до 64 байт")
		return
	}
	if utf8.RuneCountInString(req.ContextSentence) > maxContextRunes {
		writeError(w, http.StatusBadRequest, "bad_request", "context_sentence длиннее 1000 символов")
		return
	}
	sentence := explain.Context(req.ContextSentence)
	if !explain.Contains(sentence, surface) {
		writeError(w, http.StatusBadRequest, "bad_request", "context_sentence — предложение, в котором стоит слово surface")
		return
	}
	if req.LessonID != nil && !a.checkClickLesson(w, r, *req.LessonID) {
		return
	}
	ctx := r.Context()
	term, err := a.store.TermByID(ctx, termID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "термин не найден")
		return
	case err != nil:
		internalError(w, "term by id", err)
		return
	}
	// Ключ кэша — термин: форма другого слова дала бы чужое объяснение
	// под этим ключом. Лемма термина — из той же формы при клике, поэтому
	// в промпт идут только буквы, апостроф и дефис
	if lemma.Lemma(surface) != term.Lemma {
		writeError(w, http.StatusBadRequest, "bad_request", "surface — форма другого слова, не термина "+term.Lemma)
		return
	}
	hash := explain.Hash(sentence)
	cached, err := a.store.TermExplanation(ctx, term.ID, hash)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, toExplainResponse(term, cached, true))
		return
	case !errors.Is(err, store.ErrNotFound):
		internalError(w, "term explanation", err)
		return
	}

	u := userFrom(ctx)
	ok, err = a.store.ReserveLLMRequest(ctx, u.ID, store.LLMKindExplain, a.llmExplainDailyLimit)
	if err != nil {
		internalError(w, "reserve llm request", err)
		return
	}
	if !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "лимит объяснений на сегодня исчерпан")
		return
	}
	// Вызов может длиться дольше WriteTimeout сервера (20 с), как у
	// генерации текста
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(explainBudget + 5*time.Second))
	lctx, cancel := context.WithTimeout(ctx, explainBudget)
	defer cancel()
	res, err := a.llm.Generate(lctx, explain.NewRequest(surface, term.Lemma, sentence))
	var body string
	if err == nil {
		var truncated bool
		if body, truncated = explain.Finish(res.Text); truncated {
			logf("explain term %d: ответ длиннее %d символов обрезан", term.ID, explain.MaxRunes)
		}
		if body == "" {
			err = &llm.Error{Provider: a.llm.Name(), Kind: llm.ErrEmpty}
		}
	}
	switch {
	case llm.Temporary(err):
		logf("explain term %d user %d: %v", term.ID, u.ID, err)
		writeError(w, http.StatusServiceUnavailable, "llm_unavailable", "LLM временно недоступен, попробуйте позже")
		return
	case err != nil:
		logf("explain term %d user %d: %v", term.ID, u.ID, err)
		writeError(w, http.StatusBadGateway, "llm_failed", "LLM не справился с запросом")
		return
	}

	e := store.Explanation{
		TermID: term.ID, ContextHash: hash, Context: sentence, Body: body,
		Provider: a.llm.Name(), Model: res.Model,
	}
	// Оплаченный ответ сохраняем, даже если клиент уже ушёл
	if err := a.store.SaveTermExplanation(context.WithoutCancel(ctx), &e); err != nil {
		internalError(w, "save term explanation", err)
		return
	}
	writeJSON(w, http.StatusOK, toExplainResponse(term, &e, false))
}
