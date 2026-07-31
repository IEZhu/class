package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
)

type materialResponse struct {
	ID        int64     `json:"id"`
	LessonID  int64     `json:"lesson_id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	BodyMD    string    `json:"body_md"`
	CreatedAt time.Time `json:"created_at"`
	// s3_key появится с файлами в S3 (этап 1, долг D-4)
}

func toMaterialResponses(ms []store.Material) []materialResponse {
	out := make([]materialResponse, 0, len(ms))
	for _, m := range ms {
		out = append(out, materialResponse{
			ID: m.ID, LessonID: m.LessonID, Kind: m.Kind,
			Title: m.Title, BodyMD: m.BodyMD, CreatedAt: m.CreatedAt,
		})
	}
	return out
}

// handleCreateMaterial обслуживает POST /lessons/{id}/materials и /homework:
// kind определяется маршрутом (материал файлов не несёт до S1-3 — долг D-4).
func (a *API) handleCreateMaterial(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lessonID, ok := pathID(w, r)
		if !ok {
			return
		}
		var req struct {
			Title  string `json:"title"`
			BodyMD string `json:"body_md"`
		}
		if err := decodeBody(w, r, &req); err != nil || req.Title == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "title обязателен (body_md — markdown, опционален)")
			return
		}
		// материал вправе добавлять только teacher этого урока
		d, err := a.store.GetLessonDetail(r.Context(), lessonID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "урок не найден")
			return
		}
		if err != nil {
			internalError(w, "load lesson for material", err)
			return
		}
		if userFrom(r.Context()).ID != d.TeacherID {
			writeError(w, http.StatusForbidden, "forbidden", "материалы добавляет teacher этого урока")
			return
		}
		m, err := a.store.CreateMaterial(r.Context(), lessonID, kind, req.Title, req.BodyMD)
		if err != nil {
			internalError(w, "create material", err)
			return
		}
		writeJSON(w, http.StatusCreated, materialResponse{
			ID: m.ID, LessonID: m.LessonID, Kind: m.Kind,
			Title: m.Title, BodyMD: m.BodyMD, CreatedAt: m.CreatedAt,
		})
	}
}
