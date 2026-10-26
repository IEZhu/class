package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/IEZhu/class/backend/internal/assemblyai"
	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

// assemblyAIWebhook — тело вебхука: только id и итоговый статус, сам
// результат забирает worker (джоба asr_result).
type assemblyAIWebhook struct {
	TranscriptID string `json:"transcript_id"`
	Status       string `json:"status"`
}

// handleAssemblyAIWebhook — AssemblyAI сообщает итог транскрибации (S2-2).
// Аутентификация — секретный заголовок, который worker передал при
// отправке (04-api.md). Хендлер только двигает статус и ставит джобу:
// AssemblyAI ждёт 2xx не дольше 10 с, а результат часового урока весит
// мегабайты (ADR-015). На 5xx AssemblyAI повторяет доставку, на 4xx — нет.
func (a *API) handleAssemblyAIWebhook(w http.ResponseWriter, r *http.Request) {
	secret := a.cfg.AssemblyAIWebhookSecret
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "assemblyai_not_configured", "транскрибация не настроена")
		return
	}
	got := r.Header.Get(assemblyai.WebhookHeader)
	if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
		logf("assemblyai webhook: секрет не принят")
		writeError(w, http.StatusUnauthorized, "bad_secret", "секрет не принят")
		return
	}
	var req assemblyAIWebhook
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	if req.TranscriptID == "" || (req.Status != assemblyai.StatusCompleted && req.Status != assemblyai.StatusError) {
		writeError(w, http.StatusBadRequest, "bad_request", "нужны transcript_id и status completed|error")
		return
	}
	switch err := a.store.TranscriptWebhook(r.Context(), store.ProviderAssemblyAI, req.TranscriptID); {
	case errors.Is(err, store.ErrNotFound):
		// Вебхук мог обогнать коммит external_id (worker пишет его после
		// ответа AssemblyAI): 503, чтобы AssemblyAI повторил доставку.
		// Совсем потерянный итог подберёт сверка asr_poll (ADR-016)
		logf("assemblyai webhook: транскрипт %s пока неизвестен", req.TranscriptID)
		writeError(w, http.StatusServiceUnavailable, "unknown_transcript", "транскрипт пока не найден")
		return
	case err != nil:
		// 5xx — AssemblyAI повторит доставку
		internalError(w, "assemblyai webhook", err)
		return
	}
	logf("assemblyai webhook: транскрипт %s, %s", req.TranscriptID, req.Status)
	w.WriteHeader(http.StatusOK)
}

type utteranceResponse struct {
	Idx          int    `json:"idx"`
	SpeakerLabel string `json:"speaker_label"`
	// null, пока спикер не сопоставлен с участником (S2-5)
	UserID  *int64 `json:"user_id"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

type transcriptResponse struct {
	// queued | submitted | fetching — готовится; completed; error — не будет
	Status     string              `json:"status"`
	Utterances []utteranceResponse `json:"utterances"`
	// null, пока VTT не выложен или хранилище не настроено
	VTTURL       *string    `json:"vtt_url"`
	VTTExpiresAt *time.Time `json:"vtt_expires_at"`
}

// handleGetTranscript — транскрипт урока для вьюера (S2-4): реплики
// и presigned-ссылка на VTT. Граница доступа та же, что у страницы урока
// и записи: teacher урока или участник. Причину сбоя наружу не отдаём —
// в ней внутренние подробности провайдера.
func (a *API) handleGetTranscript(w http.ResponseWriter, r *http.Request) {
	lessonID, ok := pathID(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	d, err := a.store.GetLessonDetail(r.Context(), lessonID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "урок не найден")
		return
	}
	if err != nil {
		internalError(w, "get lesson", err)
		return
	}
	if !canSeeLesson(u, d) {
		writeError(w, http.StatusForbidden, "forbidden", "нет доступа к этому уроку")
		return
	}

	t, utts, err := a.store.LessonTranscript(r.Context(), lessonID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no_transcript", "транскрипта этого урока нет")
		return
	}
	if err != nil {
		internalError(w, "lesson transcript", err)
		return
	}
	resp := transcriptResponse{Status: t.Status, Utterances: make([]utteranceResponse, 0, len(utts))}
	for _, it := range utts {
		resp.Utterances = append(resp.Utterances, utteranceResponse{
			Idx: it.Idx, SpeakerLabel: it.SpeakerLabel, UserID: it.UserID,
			StartMS: it.StartMS, EndMS: it.EndMS, Text: it.Text,
		})
	}
	if t.VTTKey != "" && a.storage != nil {
		url, err := a.storage.PresignGet(r.Context(), t.VTTKey, storage.PresignTTL)
		if err != nil {
			internalError(w, "presign vtt", err)
			return
		}
		expires := time.Now().Add(storage.PresignTTL)
		resp.VTTURL, resp.VTTExpiresAt = &url, &expires
	}
	writeJSON(w, http.StatusOK, resp)
}
