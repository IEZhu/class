package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/IEZhu/class/backend/internal/store"
)

// inviteTTL — срок жизни ссылки-приглашения (ADR-008). Неделя: хватает,
// чтобы человек дошёл до ссылки, и мало, чтобы забытая ссылка жила вечно.
const inviteTTL = 7 * 24 * time.Hour

type inviteResponse struct {
	ID          int64     `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	GroupName   string    `json:"group_name"`
	InviterName string    `json:"inviter_name"`
	ExpiresAt   time.Time `json:"expires_at"`
	// URL отдаётся только в ответе на создание: в БД лежит лишь хэш токена,
	// восстановить ссылку позже нельзя — только выпустить новую.
	URL string `json:"url,omitempty"`
}

func toInviteResponse(inv store.Invite) inviteResponse {
	return inviteResponse{
		ID: inv.ID, Email: inv.Email, Name: inv.Name, Role: inv.Role,
		GroupName: inv.GroupName, InviterName: inv.InviterName, ExpiresAt: inv.ExpiresAt,
	}
}

func (a *API) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r.Context())

	var req struct {
		Email   string `json:"email"`
		Name    string `json:"name"`
		Role    string `json:"role"`
		GroupID *int64 `json:"group_id"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}

	email := normalizeEmail(req.Email)
	name := strings.TrimSpace(req.Name)
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = store.RoleStudent
	}
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, "bad_request", "нужен корректный email")
		return
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name обязателен")
		return
	}
	if !validRole(role) {
		writeError(w, http.StatusBadRequest, "bad_request", "role — admin, teacher или student")
		return
	}
	if actor.Role == store.RoleTeacher {
		if role != store.RoleStudent {
			writeError(w, http.StatusForbidden, "forbidden", "преподаватель приглашает только студентов")
			return
		}
		// Зачисление в группу — правка её состава, а это только админ (ADR-007)
		if req.GroupID != nil {
			writeError(w, http.StatusForbidden, "forbidden", "зачисление в группу — только админ")
			return
		}
	}

	// Занятый email ловим здесь, а не при переходе по ссылке: приглашённый
	// не должен упираться в 409 после того, как придумал пароль.
	switch _, err := a.store.UserByEmail(r.Context(), email); {
	case err == nil:
		writeError(w, http.StatusConflict, "conflict", "пользователь с таким email уже есть")
		return
	case !errors.Is(err, store.ErrNotFound):
		internalError(w, "check email", err)
		return
	}

	token, tokenHash, err := newToken()
	if err != nil {
		internalError(w, "new invite token", err)
		return
	}
	inv, err := a.store.CreateInvite(r.Context(), tokenHash, email, name, role,
		req.GroupID, actor.ID, time.Now().Add(inviteTTL))
	if err != nil {
		if store.PgErrorCode(err) == pgForeignKeyViolation {
			writeError(w, http.StatusNotFound, "not_found", "группа не найдена")
			return
		}
		internalError(w, "create invite", err)
		return
	}

	out := toInviteResponse(*inv)
	out.InviterName = actor.Name
	out.URL = a.cfg.PublicBaseURL + "/invite/" + token
	writeJSON(w, http.StatusCreated, out)
}

func (a *API) handleListInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := a.store.ListPendingInvites(r.Context(), inviteScope(userFrom(r.Context())))
	if err != nil {
		internalError(w, "list invites", err)
		return
	}
	out := make([]inviteResponse, 0, len(invites))
	for _, inv := range invites {
		out = append(out, toInviteResponse(inv))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	switch err := a.store.DeleteInvite(r.Context(), id, inviteScope(userFrom(r.Context()))); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "приглашение не найдено")
	case err != nil:
		internalError(w, "revoke invite", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleInvitePreview — публичный: страница приглашения показывает, кого и
// куда зовут, до того как человек придумает пароль.
func (a *API) handleInvitePreview(w http.ResponseWriter, r *http.Request) {
	inv, err := a.store.InviteByTokenHash(r.Context(), hashToken(r.PathValue("token")))
	if !a.writeInviteLookupError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, toInviteResponse(*inv))
}

// handleAcceptInvite — публичный: приглашённый задаёт себе пароль, получает
// учётку и сессию. Пароль администратору не известен — в этом весь смысл
// ссылок (ADR-008).
func (a *API) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	if err := validPassword(w, req.Password); err != nil {
		return
	}

	tokenHash := hashToken(r.PathValue("token"))
	// Предпроверка — чтобы отличить «нет такой ссылки» от «просрочена»;
	// гонку она не решает, за одноразовость отвечает UPDATE в AcceptInvite.
	if _, err := a.store.InviteByTokenHash(r.Context(), tokenHash); !a.writeInviteLookupError(w, err) {
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		internalError(w, "hash password", err)
		return
	}
	u, err := a.store.AcceptInvite(r.Context(), tokenHash, string(hash))
	switch {
	case errors.Is(err, store.ErrInviteUnusable):
		writeError(w, http.StatusGone, "invite_used", "ссылка уже использована или просрочена")
		return
	case store.PgErrorCode(err) == pgUniqueViolation:
		writeError(w, http.StatusConflict, "conflict", "пользователь с таким email уже есть")
		return
	case err != nil:
		internalError(w, "accept invite", err)
		return
	}

	token, tokenHash, err := newToken()
	if err != nil {
		internalError(w, "new session", err)
		return
	}
	if err := a.store.CreateSession(r.Context(), u.ID, tokenHash, time.Now().Add(sessionTTL)); err != nil {
		internalError(w, "create session", err)
		return
	}
	http.SetCookie(w, sessionCookieFor(token, int(sessionTTL.Seconds())))
	writeJSON(w, http.StatusCreated, userResponse{ID: u.ID, Email: u.Email, Role: u.Role, Name: u.Name})
}

// writeInviteLookupError — общий разбор ошибок поиска приглашения.
// false — ответ уже записан, обработку продолжать нельзя.
func (a *API) writeInviteLookupError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "приглашение не найдено")
		return false
	case errors.Is(err, store.ErrInviteUnusable):
		writeError(w, http.StatusGone, "invite_used", "ссылка уже использована или просрочена")
		return false
	case err != nil:
		internalError(w, "invite lookup", err)
		return false
	}
	return true
}

// inviteScope — 0 для админа (все приглашения), свой id для преподавателя.
func inviteScope(u *store.User) int64 {
	if u.Role == store.RoleAdmin {
		return 0
	}
	return u.ID
}
