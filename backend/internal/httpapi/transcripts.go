package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
		logf("assemblyai webhook: транскрипт %s нам неизвестен", req.TranscriptID)
		writeError(w, http.StatusNotFound, "not_found", "транскрипт не найден")
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
	// null, пока спикер не сопоставлен с участником (S2-5); speaker_name —
	// его имя, вьюер показывает его вместо метки
	UserID      *int64  `json:"user_id"`
	SpeakerName *string `json:"speaker_name"`
	StartMS     int64   `json:"start_ms"`
	EndMS       int64   `json:"end_ms"`
	Text        string  `json:"text"`
	// null — текст как его распознал ASR; иначе время правки (S2-4)
	EditedAt *time.Time `json:"edited_at"`
}

func toUtteranceResponse(u store.Utterance) utteranceResponse {
	return utteranceResponse{
		Idx: u.Idx, SpeakerLabel: u.SpeakerLabel, UserID: u.UserID, SpeakerName: u.SpeakerName,
		StartMS: u.StartMS, EndMS: u.EndMS, Text: u.Text, EditedAt: u.EditedAt,
	}
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
		resp.Utterances = append(resp.Utterances, toUtteranceResponse(it))
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

// maxUtteranceRunes — потолок текста реплики: с запасом на долгий монолог
// одного спикера (около получаса речи).
const maxUtteranceRunes = 20000

// handleEditUtterance — teacher урока исправляет ошибку ASR в реплике
// (S2-4, ADR-025). Пробелы схлопываются: реплика — одна строка речи,
// VTT всё равно сводит переводы строк в пробел. Тот же текст — 200 без
// новой отметки правки и без перерендера VTT.
func (a *API) handleEditUtterance(w http.ResponseWriter, r *http.Request) {
	lessonID, ok := pathID(w, r)
	if !ok {
		return
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil || idx < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "некорректный idx в пути")
		return
	}
	if !a.transcriptOwner(w, r, lessonID) {
		return
	}
	var req struct {
		Text *string `json:"text"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	if req.Text == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "нужен text")
		return
	}
	text := strings.Join(strings.Fields(*req.Text), " ")
	switch {
	case text == "":
		writeError(w, http.StatusBadRequest, "bad_request", "текст реплики пуст")
		return
	case strings.ContainsRune(text, 0):
		// Postgres не принимает NUL в text, а JSON его пропускает (\u0000)
		writeError(w, http.StatusBadRequest, "bad_request", "в тексте недопустимый символ")
		return
	case utf8.RuneCountInString(text) > maxUtteranceRunes:
		writeError(w, http.StatusBadRequest, "bad_request",
			"текст реплики длиннее "+strconv.Itoa(maxUtteranceRunes)+" символов")
		return
	}
	u, err := a.store.EditUtterance(r.Context(), lessonID, idx, text, userFrom(r.Context()).ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no_utterance", "такой реплики в транскрипте урока нет")
		return
	}
	if err != nil {
		internalError(w, "edit utterance", err)
		return
	}
	writeJSON(w, http.StatusOK, toUtteranceResponse(*u))
}

type speakerResponse struct {
	SpeakerLabel string  `json:"speaker_label"`
	UserID       *int64  `json:"user_id"`
	SpeakerName  *string `json:"speaker_name"`
}

// handleMapSpeaker — teacher урока сопоставляет метку спикера (A, B, C…)
// с человеком урока: собой или участником снапшота (S2-5, ADR-025).
// Тело {user_id: N | null}; null снимает маппинг. Ключ обязателен: пустое
// тело не должно молча стирать маппинг.
func (a *API) handleMapSpeaker(w http.ResponseWriter, r *http.Request) {
	lessonID, ok := pathID(w, r)
	if !ok {
		return
	}
	label := r.PathValue("label")
	if !a.transcriptOwner(w, r, lessonID) {
		return
	}
	var req struct {
		UserID json.RawMessage `json:"user_id"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	var userID *int64
	if err := json.Unmarshal(req.UserID, &userID); len(req.UserID) == 0 || err != nil || (userID != nil && *userID <= 0) {
		writeError(w, http.StatusBadRequest, "bad_request", "нужен user_id: id участника урока или null")
		return
	}
	name, err := a.store.MapSpeaker(r.Context(), lessonID, label, userID)
	switch {
	case errors.Is(err, store.ErrNotParticipant):
		writeError(w, http.StatusBadRequest, "not_participant", "спикера можно сопоставить только с участником урока")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no_speaker", "такого спикера в транскрипте урока нет")
		return
	case err != nil:
		internalError(w, "map speaker", err)
		return
	}
	writeJSON(w, http.StatusOK, speakerResponse{SpeakerLabel: label, UserID: userID, SpeakerName: name})
}

// transcriptOwner — правит транскрипт только teacher урока: урока нет —
// 404, чужой урок — 403. false — ответ уже записан.
func (a *API) transcriptOwner(w http.ResponseWriter, r *http.Request, lessonID int64) bool {
	teacherID, err := a.store.LessonTeacherID(r.Context(), lessonID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "урок не найден")
		return false
	}
	if err != nil {
		internalError(w, "load lesson for transcript", err)
		return false
	}
	if userFrom(r.Context()).ID != teacherID {
		writeError(w, http.StatusForbidden, "forbidden", "транскрипт правит teacher этого урока")
		return false
	}
	return true
}
