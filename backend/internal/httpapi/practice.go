package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/IEZhu/class/backend/internal/lemma"
	"github.com/IEZhu/class/backend/internal/llm"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/textgen"
)

const (
	// DefaultLLMDailyLimit — генераций на пользователя в сутки, если
	// LLM_DAILY_LIMIT не задан (смета — ADR-029).
	DefaultLLMDailyLimit = 10
	// generateBudget — потолок всей генерации: черновик и ретрай по
	// llm.Timeout с запасом на базу; укладывается в таймаут прокси перед
	// api (ADR-029).
	generateBudget = 2*llm.Timeout + 5*time.Second
	// maxTextHistory — сколько своих текстов отдаёт GET /practice/texts
	maxTextHistory = 50
)

type generateTextRequest struct {
	// Words — целевые слова; пусто — из личного словаря (S3-5, ADR-030)
	Words    []string `json:"words"`
	Level    string   `json:"level"`
	Genre    string   `json:"genre"`
	MaxWords int      `json:"max_words"`
}

type generatedTextResponse struct {
	ID           int64          `json:"id"`
	Level        string         `json:"level"`
	Genre        string         `json:"genre"`
	MaxWords     int            `json:"max_words"`
	TargetLemmas []string       `json:"target_lemmas"`
	Body         string         `json:"body"`
	WordCount    int            `json:"word_count"`
	Coverage     float64        `json:"coverage"`
	Occurrences  map[string]int `json:"occurrences"`
	UnknownShare float64        `json:"unknown_share"`
	Attempts     int            `json:"attempts"`
	Provider     string         `json:"provider"`
	Model        string         `json:"model"`
	CreatedAt    time.Time      `json:"created_at"`
}

func toGeneratedTextResponse(t store.GeneratedText) generatedTextResponse {
	return generatedTextResponse{
		ID: t.ID, Level: t.Level, Genre: t.Genre, MaxWords: t.MaxWords, TargetLemmas: t.TargetLemmas,
		Body: t.Body, WordCount: t.WordCount, Coverage: t.Coverage, Occurrences: t.Occurrences,
		UnknownShare: t.UnknownShare, Attempts: t.Attempts, Provider: t.Provider, Model: t.Model,
		CreatedAt: t.CreatedAt,
	}
}

var errNoUserTerms = errors.New("в личном словаре нет невыученных слов")

// userTermWords — целевые слова из личного словаря вызывающего (S3-5),
// когда список в запросе пуст: новые и «забытые» невыученные слова
// (store.PracticeLemmas), столько, сколько уместит текст длиной maxWords.
// Лемма, которую NewParams не примет за слово, пропускается: иначе одна
// такая строка словаря отвечала бы 400 на каждый запрос без списка.
func (a *API) userTermWords(ctx context.Context, userID int64, maxWords int) ([]string, error) {
	lemmas, err := a.store.PracticeLemmas(ctx, userID, termLang, textgen.TargetsFor(maxWords))
	if err != nil {
		return nil, err
	}
	words := lemmas[:0]
	for _, l := range lemmas {
		if lemma.IsWord(l) {
			words = append(words, l)
		}
	}
	if len(words) == 0 {
		return nil, errNoUserTerms
	}
	return words, nil
}

// handleGenerateText — POST /practice/generate-text (S5-6,
// integrations/llm.md): слова, уровень, жанр, длина → промпт, где
// слова — данные → валидатор покрытия, один ретрай → generated_texts.
// Без ключа LLM — 503; место в суточном лимите занимается до вызова
// провайдера и не возвращается при сбое: неудачный вызов тоже мог
// стоить денег.
func (a *API) handleGenerateText(w http.ResponseWriter, r *http.Request) {
	if a.llm == nil {
		writeError(w, http.StatusServiceUnavailable, "llm_not_configured", "LLM не настроен")
		return
	}
	// Генерация дольше WriteTimeout сервера (cmd/api: 20 с) — продлеваем
	// дедлайн записи только этому ответу. ReadTimeout не мешает: после
	// чтения тела net/http снимает дедлайн чтения. Не все ResponseWriter
	// это умеют (httptest.ResponseRecorder), там дедлайна и так нет
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(generateBudget + 5*time.Second))
	var req generateTextRequest
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "некорректное тело запроса")
		return
	}
	u := userFrom(r.Context())
	words := req.Words
	if len(words) == 0 {
		fromTerms, err := a.userTermWords(r.Context(), u.ID, req.MaxWords)
		switch {
		case errors.Is(err, errNoUserTerms):
			writeError(w, http.StatusBadRequest, "bad_request",
				"words — укажите целевые слова: в личном словаре нет невыученных слов")
			return
		case err != nil:
			internalError(w, "user term words", err)
			return
		}
		words = fromTerms
	}
	// NewParams возвращает только *textgen.InputError — текст для 400
	params, err := textgen.NewParams(words, req.Level, req.Genre, req.MaxWords)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ok, err := a.store.ReserveLLMRequest(r.Context(), u.ID, a.llmDailyLimit)
	if err != nil {
		internalError(w, "reserve llm request", err)
		return
	}
	if !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "лимит генераций на сегодня исчерпан")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), generateBudget)
	defer cancel()
	res, err := textgen.Generate(ctx, a.llm, params)
	var coverageErr *textgen.CoverageError
	switch {
	case errors.As(err, &coverageErr):
		logf("generate text user %d: %v", u.ID, err)
		rep := coverageErr.Report
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":    "модель дважды не использовала все слова — попробуйте другой набор или длину",
			"code":     "coverage_failed",
			"missing":  rep.Missing,
			"coverage": rep.Coverage,
		})
		return
	case llm.Temporary(err):
		logf("generate text user %d: %v", u.ID, err)
		writeError(w, http.StatusServiceUnavailable, "llm_unavailable", "LLM временно недоступен, попробуйте позже")
		return
	case err != nil:
		logf("generate text user %d: %v", u.ID, err)
		writeError(w, http.StatusBadGateway, "llm_failed", "LLM не справился с запросом")
		return
	}

	t := store.GeneratedText{
		UserID: u.ID, Level: params.Level, Genre: params.Genre, MaxWords: params.MaxWords,
		TargetLemmas: params.Lemmas(), Body: res.Text, WordCount: res.Report.Words,
		Coverage: res.Report.Coverage, Occurrences: res.Report.Occurrences,
		UnknownShare: res.Report.UnknownShare, Attempts: res.Attempts, Provider: res.Provider, Model: res.Model,
	}
	// Оплаченный текст сохраняем, даже если клиент уже ушёл
	if err := a.store.CreateGeneratedText(context.WithoutCancel(r.Context()), &t); err != nil {
		internalError(w, "create generated text", err)
		return
	}
	writeJSON(w, http.StatusCreated, toGeneratedTextResponse(t))
}

// handleListGeneratedTexts — GET /practice/texts: свои тексты, новые
// первыми. Поиск по словам среди всех текстов и Tatoeba (?words=) —
// S5-7.
func (a *API) handleListGeneratedTexts(w http.ResponseWriter, r *http.Request) {
	texts, err := a.store.GeneratedTextsByUser(r.Context(), userFrom(r.Context()).ID, maxTextHistory)
	if err != nil {
		internalError(w, "list generated texts", err)
		return
	}
	out := make([]generatedTextResponse, 0, len(texts))
	for _, t := range texts {
		out = append(out, toGeneratedTextResponse(t))
	}
	writeJSON(w, http.StatusOK, out)
}
