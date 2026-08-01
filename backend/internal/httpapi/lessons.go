package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
)

type lessonResponse struct {
	ID        int64     `json:"id"`
	GroupID   int64     `json:"group_id"`
	TeacherID int64     `json:"teacher_id"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Status    string    `json:"status"`
	GroupName string    `json:"group_name,omitempty"`
}

type lessonDetailResponse struct {
	lessonResponse
	TeacherName  string             `json:"teacher_name"`
	Materials    []materialResponse `json:"materials"`
	Participants []memberResponse   `json:"participants"`
}

type lessonTimesRequest struct {
	GroupID  int64     `json:"group_id,omitempty"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
}

func (req *lessonTimesRequest) validTimes() bool {
	return !req.StartsAt.IsZero() && !req.EndsAt.IsZero() && req.EndsAt.After(req.StartsAt)
}

func (a *API) handleCreateLesson(w http.ResponseWriter, r *http.Request) {
	var req lessonTimesRequest
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	if req.GroupID <= 0 || !req.validTimes() {
		writeError(w, http.StatusBadRequest, "bad_request", "нужны group_id и starts_at < ends_at (RFC3339)")
		return
	}
	u := userFrom(r.Context())
	l, err := a.store.CreateLesson(r.Context(), req.GroupID, u.ID, req.StartsAt, req.EndsAt)
	if err != nil {
		if store.PgErrorCode(err) == pgForeignKeyViolation {
			writeError(w, http.StatusNotFound, "not_found", "группа не найдена")
			return
		}
		internalError(w, "create lesson", err)
		return
	}
	writeJSON(w, http.StatusCreated, lessonToResponse(l, ""))
}

func (a *API) handleListLessons(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListLessonsForUser(r.Context(), userFrom(r.Context()))
	if err != nil {
		internalError(w, "list lessons", err)
		return
	}
	out := make([]lessonResponse, 0, len(items))
	for _, it := range items {
		out = append(out, lessonToResponse(&it.Lesson, it.GroupName))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetLesson(w http.ResponseWriter, r *http.Request) {
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
	resp := lessonDetailResponse{
		lessonResponse: lessonToResponse(&d.Lesson, d.GroupName),
		TeacherName:    d.TeacherName,
		Materials:      toMaterialResponses(d.Materials),
		Participants:   toMemberResponses(d.Participants),
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) handleRescheduleLesson(w http.ResponseWriter, r *http.Request) {
	lessonID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req lessonTimesRequest
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	if !req.validTimes() {
		writeError(w, http.StatusBadRequest, "bad_request", "нужны starts_at < ends_at (RFC3339)")
		return
	}
	u := userFrom(r.Context())
	l, err := a.store.RescheduleLesson(r.Context(), lessonID, u.ID, req.StartsAt, req.EndsAt)
	if err != nil {
		writeLessonUpdateError(w, "reschedule lesson", err)
		return
	}
	writeJSON(w, http.StatusOK, lessonToResponse(l, ""))
}

func (a *API) handleCancelLesson(w http.ResponseWriter, r *http.Request) {
	lessonID, ok := pathID(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if err := a.store.CancelLesson(r.Context(), lessonID, u.ID); err != nil {
		writeLessonUpdateError(w, "cancel lesson", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeLessonUpdateError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "урок не найден")
	case errors.Is(err, store.ErrNotOwner):
		writeError(w, http.StatusForbidden, "forbidden", "урок ведёт другой преподаватель")
	case errors.Is(err, store.ErrLessonNotEditable):
		// 409 для конфликтов состояния (04-api.md)
		writeError(w, http.StatusConflict, "conflict", "урок уже не в статусе scheduled")
	default:
		internalError(w, op, err)
	}
}

// canSeeLesson — доступ по 04-api.md: teacher урока или участник снапшота.
func canSeeLesson(u *store.User, d *store.LessonDetail) bool {
	if u.ID == d.TeacherID {
		return true
	}
	for _, p := range d.Participants {
		if p.UserID == u.ID {
			return true
		}
	}
	return false
}

func lessonToResponse(l *store.Lesson, groupName string) lessonResponse {
	return lessonResponse{
		ID: l.ID, GroupID: l.GroupID, TeacherID: l.TeacherID,
		StartsAt: l.StartsAt, EndsAt: l.EndsAt, Status: l.Status, GroupName: groupName,
	}
}

// pathID — {id} из пути; 0/мусор → 400 и false.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "некорректный id в пути")
		return 0, false
	}
	return id, true
}
