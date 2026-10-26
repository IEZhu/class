package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

type mediaURLResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// handleMediaURL — presigned-ссылка на запись урока. Файл идёт хранилище ↔
// браузер напрямую, api только проверяет право и подписывает ссылку.
func (a *API) handleMediaURL(w http.ResponseWriter, r *http.Request) {
	if a.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_not_configured", "хранилище не настроено")
		return
	}
	lessonID, ok := pathIDNamed(w, r, "lesson_id")
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
	// Та же граница, что у страницы урока: запись видят только его участники
	if !canSeeLesson(u, d) {
		writeError(w, http.StatusForbidden, "forbidden", "нет доступа к этому уроку")
		return
	}

	key, err := a.store.LessonRecordingKey(r.Context(), lessonID)
	if err != nil {
		internalError(w, "recording key", err)
		return
	}
	if key == "" {
		writeError(w, http.StatusNotFound, "no_recording", "записи этого урока нет")
		return
	}

	url, err := a.storage.PresignGet(r.Context(), key, storage.PresignTTL)
	if err != nil {
		internalError(w, "presign", err)
		return
	}
	writeJSON(w, http.StatusOK, mediaURLResponse{
		URL:       url,
		ExpiresAt: time.Now().Add(storage.PresignTTL),
	})
}
