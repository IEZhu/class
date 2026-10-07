package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
)

// guestSessionGrace — сколько сессия гостя живёт после конца события.
// Нужен запас на переподключение и на то, что урок затянулся.
const guestSessionGrace = 2 * time.Hour

// maxGuestNameLen — имя гость вводит сам, и оно попадёт в комнату
// и в транскрипт; ограничиваем, чтобы не растянуть вёрстку.
const maxGuestNameLen = 60

type guestLinkResponse struct {
	URL string `json:"url"`
}

type guestEventResponse struct {
	Title       string    `json:"title"`
	TeacherName string    `json:"teacher_name"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	// Гость попадает в запись и транскрипт — страница входа обязана
	// сказать это до того, как он представится (ADR-010).
	Recorded bool `json:"recorded"`
}

// handleIssueGuestLink — выпуск или перевыпуск ссылки на событие.
// Перевыпуск обнуляет старую: разошлась не туда — сделал новую.
func (a *API) handleIssueGuestLink(w http.ResponseWriter, r *http.Request) {
	lessonID, ok := a.lessonForOwner(w, r)
	if !ok {
		return
	}
	token, tokenHash, err := newToken()
	if err != nil {
		internalError(w, "new guest token", err)
		return
	}
	if err := a.store.SetGuestToken(r.Context(), lessonID, tokenHash); err != nil {
		internalError(w, "set guest token", err)
		return
	}
	writeJSON(w, http.StatusCreated, guestLinkResponse{URL: a.cfg.PublicBaseURL + "/join/" + token})
}

func (a *API) handleRevokeGuestLink(w http.ResponseWriter, r *http.Request) {
	lessonID, ok := a.lessonForOwner(w, r)
	if !ok {
		return
	}
	if err := a.store.SetGuestToken(r.Context(), lessonID, ""); err != nil {
		internalError(w, "revoke guest token", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGuestEvent — публичный предпросмотр: куда человека зовут и что
// событие пишется. Аутентификации у него ещё нет.
func (a *API) handleGuestEvent(w http.ResponseWriter, r *http.Request) {
	e, err := a.store.GuestEventByToken(r.Context(), hashToken(r.PathValue("token")))
	if !writeGuestLookupError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, guestEventResponse{
		Title: e.Title, TeacherName: e.TeacherName,
		StartsAt: e.StartsAt, EndsAt: e.EndsAt,
		Recorded: true,
	})
}

// handleGuestJoin — публичный: человек представился и стал участником
// события. Учётка гостя без пароля, войти повторно нельзя (ADR-010).
func (a *API) handleGuestJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "нужно имя")
		return
	}
	if len([]rune(name)) > maxGuestNameLen {
		writeError(w, http.StatusBadRequest, "bad_request", "имя слишком длинное")
		return
	}

	tokenHash := hashToken(r.PathValue("token"))
	e, err := a.store.GuestEventByToken(r.Context(), tokenHash)
	if !writeGuestLookupError(w, err) {
		return
	}

	sessionToken, sessionTokenHash, err := newToken()
	if err != nil {
		internalError(w, "new session", err)
		return
	}
	u, lessonID, err := a.store.JoinAsGuest(r.Context(), tokenHash, name,
		guestEmail(sessionTokenHash), sessionTokenHash, e.EndsAt.Add(guestSessionGrace))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "событие не найдено")
		return
	case errors.Is(err, store.ErrEventOver):
		writeError(w, http.StatusGone, "event_over", "событие уже закончилось")
		return
	case err != nil:
		internalError(w, "join as guest", err)
		return
	}

	http.SetCookie(w, sessionCookieFor(sessionToken, int(time.Until(e.EndsAt.Add(guestSessionGrace)).Seconds())))
	writeJSON(w, http.StatusCreated, struct {
		userResponse
		LessonID int64 `json:"lesson_id"`
	}{
		userResponse: userResponse{ID: u.ID, Email: u.Email, Role: u.Role, Name: u.Name},
		LessonID:     lessonID,
	})
}

// guestEmail — синтетический адрес: колонка UNIQUE NOT NULL, а настоящего
// адреса у гостя мы не спрашиваем. Берём хэш его сессии — он уже уникален.
func guestEmail(sessionTokenHash string) string {
	return "guest+" + sessionTokenHash[:32] + "@guests.local"
}

// lessonForOwner — гостевой ссылкой распоряжается преподаватель события
// или админ. Возвращает id урока; ответ уже записан, если доступа нет.
func (a *API) lessonForOwner(w http.ResponseWriter, r *http.Request) (int64, bool) {
	lessonID, ok := pathID(w, r)
	if !ok {
		return 0, false
	}
	teacherID, err := a.store.LessonTeacherID(r.Context(), lessonID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "урок не найден")
		return 0, false
	}
	if err != nil {
		internalError(w, "lesson owner", err)
		return 0, false
	}
	u := userFrom(r.Context())
	if u.Role != store.RoleAdmin && u.ID != teacherID {
		writeError(w, http.StatusForbidden, "forbidden", "это не ваше событие")
		return 0, false
	}
	return lessonID, true
}

// writeGuestLookupError — false, если ответ уже записан.
func writeGuestLookupError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "событие не найдено")
		return false
	case errors.Is(err, store.ErrEventOver):
		writeError(w, http.StatusGone, "event_over", "событие уже закончилось")
		return false
	case err != nil:
		internalError(w, "guest event", err)
		return false
	}
	return true
}
