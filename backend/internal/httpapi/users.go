package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/IEZhu/class/backend/internal/store"
)

// minPasswordLen — нижняя граница стартового и нового пароля (ADR-007).
// Пароли раздаются людьми лично, единственная защита — длина.
const minPasswordLen = 8

type userWithGroupsResponse struct {
	userResponse
	Groups []string `json:"groups"`
}

// canManage — может ли актор править учётку target (ADR-007): admin — любую,
// teacher — только студента из своих групп, остальные — ничью.
func (a *API) canManage(r *http.Request, actor *store.User, targetID int64) (bool, error) {
	switch actor.Role {
	case store.RoleAdmin:
		return true, nil
	case store.RoleTeacher:
		return a.store.TeacherManagesUser(r.Context(), actor.ID, targetID)
	default:
		return false, nil
	}
}

func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r.Context())

	var (
		users []store.UserWithGroups
		err   error
	)
	if actor.Role == store.RoleAdmin {
		users, err = a.store.ListUsers(r.Context())
	} else {
		users, err = a.store.ListUsersOfTeacherGroups(r.Context(), actor.ID)
	}
	if err != nil {
		internalError(w, "list users", err)
		return
	}

	out := make([]userWithGroupsResponse, 0, len(users))
	for _, u := range users {
		out = append(out, userWithGroupsResponse{
			userResponse: userResponse{ID: u.ID, Email: u.Email, Role: u.Role, Name: u.Name},
			Groups:       u.Groups,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r.Context())

	var req struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Role     string `json:"role"`
		Password string `json:"password"`
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
	// Преподаватель заводит только студентов: заведение равноправных —
	// эскалация привилегий через веб (ADR-007).
	if actor.Role == store.RoleTeacher && role != store.RoleStudent {
		writeError(w, http.StatusForbidden, "forbidden", "преподаватель заводит только студентов")
		return
	}
	if err := validPassword(w, req.Password); err != nil {
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		internalError(w, "hash password", err)
		return
	}
	u, err := a.store.CreateUser(r.Context(), email, role, name, string(hash))
	if err != nil {
		if store.PgErrorCode(err) == pgUniqueViolation {
			writeError(w, http.StatusConflict, "conflict", "пользователь с таким email уже есть")
			return
		}
		internalError(w, "create user", err)
		return
	}
	writeJSON(w, http.StatusCreated, userResponse{ID: u.ID, Email: u.Email, Role: u.Role, Name: u.Name})
}

func (a *API) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	name := strings.TrimSpace(req.Name)
	role := strings.TrimSpace(req.Role)
	if name == "" && role == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "нечего менять: нужен name или role")
		return
	}
	if role != "" {
		if actor.Role != store.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "роль меняет только админ")
			return
		}
		if !validRole(role) {
			writeError(w, http.StatusBadRequest, "bad_request", "role — admin, teacher или student")
			return
		}
		// Снятие роли с самого себя оставило бы систему без админа,
		// если он последний — правку своей роли запрещаем целиком.
		if id == actor.ID && role != store.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "нельзя снять админскую роль с себя")
			return
		}
	}
	if !a.authorizeManage(w, r, actor, id) {
		return
	}

	u, err := a.store.UpdateUserProfile(r.Context(), id, name, role)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "пользователь не найден")
	case err != nil:
		internalError(w, "update user", err)
	default:
		writeJSON(w, http.StatusOK, userResponse{ID: u.ID, Email: u.Email, Role: u.Role, Name: u.Name})
	}
}

// handleResetPassword — сброс пароля чужой учётке: старый не спрашиваем,
// но все сессии владельца гасятся (store.SetPassword).
func (a *API) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}

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
	if !a.authorizeManage(w, r, actor, id) {
		return
	}
	a.setPassword(w, r, id, req.Password, "reset password")
}

// handleChangeOwnPassword — смена своего пароля: старый обязателен, иначе
// перехваченный cookie позволял бы навсегда закрепиться в учётке.
func (a *API) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r.Context())

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	if err := validPassword(w, req.NewPassword); err != nil {
		return
	}

	// requireUser кладёт в контекст учётку без хэша — перечитываем.
	full, err := a.store.UserByID(r.Context(), actor.ID)
	if err != nil {
		internalError(w, "load user", err)
		return
	}
	if full.PasswordHash == "" ||
		bcrypt.CompareHashAndPassword([]byte(full.PasswordHash), []byte(req.CurrentPassword)) != nil {
		writeError(w, http.StatusForbidden, "invalid_credentials", "текущий пароль неверен")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcryptCost)
	if err != nil {
		internalError(w, "hash password", err)
		return
	}
	if err := a.store.SetPassword(r.Context(), actor.ID, string(hash)); err != nil {
		internalError(w, "change password", err)
		return
	}
	// SetPassword гасит все сессии владельца, включая текущую — выдаём
	// свежую взамен: сменивший пароль остаётся в системе, прочие
	// устройства разлогинены.
	token, tokenHash, err := newSessionToken()
	if err != nil {
		internalError(w, "new session", err)
		return
	}
	if err := a.store.CreateSession(r.Context(), actor.ID, tokenHash, time.Now().Add(sessionTTL)); err != nil {
		internalError(w, "create session", err)
		return
	}
	http.SetCookie(w, sessionCookieFor(token, int(sessionTTL.Seconds())))
	w.WriteHeader(http.StatusNoContent)
}

// handleUpdateMe — правка своего имени; email и роль себе не меняют.
func (a *API) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r.Context())

	var req struct {
		Name string `json:"name"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name обязателен")
		return
	}
	u, err := a.store.UpdateUserProfile(r.Context(), actor.ID, name, "")
	if err != nil {
		internalError(w, "update me", err)
		return
	}
	writeJSON(w, http.StatusOK, userResponse{ID: u.ID, Email: u.Email, Role: u.Role, Name: u.Name})
}

func (a *API) handleRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	groupID, ok := pathID(w, r)
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "некорректный user_id в пути")
		return
	}
	if err := a.store.RemoveGroupMember(r.Context(), groupID, userID); err != nil {
		internalError(w, "remove group member", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setPassword — общий хвост сброса и смены пароля.
func (a *API) setPassword(w http.ResponseWriter, r *http.Request, userID int64, password, op string) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		internalError(w, "hash password", err)
		return
	}
	switch err := a.store.SetPassword(r.Context(), userID, string(hash)); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "пользователь не найден")
	case err != nil:
		internalError(w, op, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// authorizeManage — 403/500 при отсутствии прав на чужую учётку;
// true, если можно продолжать.
func (a *API) authorizeManage(w http.ResponseWriter, r *http.Request, actor *store.User, targetID int64) bool {
	ok, err := a.canManage(r, actor, targetID)
	if err != nil {
		internalError(w, "check manage rights", err)
		return false
	}
	if !ok {
		writeError(w, http.StatusForbidden, "forbidden", "этот пользователь не в ваших группах")
		return false
	}
	return true
}

// validPassword пишет ответ и возвращает ошибку, если пароль короче минимума.
func validPassword(w http.ResponseWriter, password string) error {
	if len([]rune(password)) < minPasswordLen {
		err := errors.New("password too short")
		writeError(w, http.StatusBadRequest, "bad_request",
			"пароль — минимум "+strconv.Itoa(minPasswordLen)+" символов")
		return err
	}
	return nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validEmail — намеренно грубая проверка: единственный настоящий валидатор
// адреса — доставленное письмо, а почты в стеке нет (ADR-007).
func validEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	return at > 0 && at < len(email)-1 && !strings.ContainsAny(email, " \t\n")
}

func validRole(role string) bool {
	return role == store.RoleAdmin || role == store.RoleTeacher || role == store.RoleStudent
}
