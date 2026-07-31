package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/IEZhu/class/backend/internal/store"
)

const (
	sessionCookie = "sid"
	sessionTTL    = 30 * 24 * time.Hour // ADR-006
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userResponse struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
	Name  string `json:"name"`
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "email и password обязательны")
		return
	}
	u, err := a.store.UserByEmail(r.Context(), req.Email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal", "внутренняя ошибка")
		return
	}
	// Единый ответ для «нет пользователя», «пароль не задан» и «пароль не
	// подошёл» — не раскрывать существование аккаунта.
	if err != nil || u.PasswordHash == "" ||
		bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "неверный email или пароль")
		return
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "внутренняя ошибка")
		return
	}
	if err := a.store.DeleteExpiredSessions(r.Context(), u.ID); err != nil {
		// уборка не критична для логина — только лог
		logf("login: cleanup expired sessions: %v", err)
	}
	if err := a.store.CreateSession(r.Context(), u.ID, tokenHash, time.Now().Add(sessionTTL)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "внутренняя ошибка")
		return
	}
	http.SetCookie(w, sessionCookieFor(token, int(sessionTTL.Seconds())))
	writeJSON(w, http.StatusOK, userResponse{ID: u.ID, Email: u.Email, Role: u.Role, Name: u.Name})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := a.store.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			logf("logout: delete session: %v", err)
		}
	}
	http.SetCookie(w, sessionCookieFor("", -1))
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	writeJSON(w, http.StatusOK, userResponse{ID: u.ID, Email: u.Email, Role: u.Role, Name: u.Name})
}

func sessionCookieFor(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}
