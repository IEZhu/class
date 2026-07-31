// Package httpapi — HTTP-слой api: роуты, middleware, хендлеры.
// Пути без префикса /api: его стрипает caddy (docs/architecture/04-api.md).
package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"

	"github.com/IEZhu/class/backend/internal/store"
)

type API struct {
	store *store.Store
}

func New(s *store.Store) *API { return &API{store: s} }

func (a *API) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.HandleFunc("POST /auth/login", a.handleLogin)
	mux.HandleFunc("POST /auth/logout", a.handleLogout)
	mux.Handle("GET /auth/me", a.requireUser(http.HandlerFunc(a.handleMe)))

	teacher := func(h http.HandlerFunc) http.Handler { return a.requireRole("teacher", h) }
	user := func(h http.HandlerFunc) http.Handler { return a.requireUser(h) }

	mux.Handle("POST /groups", teacher(a.handleCreateGroup))
	mux.Handle("GET /groups", teacher(a.handleListGroups))
	mux.Handle("POST /groups/{id}/members", teacher(a.handleAddGroupMember))

	mux.Handle("POST /lessons", teacher(a.handleCreateLesson))
	mux.Handle("GET /lessons", user(a.handleListLessons))
	mux.Handle("GET /lessons/{id}", user(a.handleGetLesson))
	mux.Handle("PATCH /lessons/{id}", teacher(a.handleRescheduleLesson))
	mux.Handle("DELETE /lessons/{id}", teacher(a.handleCancelLesson))

	mux.Handle("POST /lessons/{id}/materials", teacher(a.handleCreateMaterial("material")))
	mux.Handle("POST /lessons/{id}/homework", teacher(a.handleCreateMaterial("homework")))
	return mux
}

const (
	maxBodyBytes = 1 << 16 // 64 KiB — с запасом для любого JSON-пейлоада этапа 0

	// SQLSTATE-коды Postgres для маппинга в HTTP (store.PgErrorCode)
	pgForeignKeyViolation = "23503"
	pgCheckViolation      = "23514"
)

// internalError — единый ответ 500 + лог реальной причины.
func internalError(w http.ResponseWriter, op string, err error) {
	logf("%s: %v", op, err)
	writeError(w, http.StatusInternalServerError, "internal", "внутренняя ошибка")
}

func (a *API) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			log.Printf("httpapi: encode response: %v", err)
		}
	}
}

// writeError — формат ошибок {error, code} из 04-api.md.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func newSessionToken() (token, tokenHash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(b)
	return token, hashToken(token), nil
}

// hashToken — в БД хранится только sha256-дайджест токена (ADR-006).
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
