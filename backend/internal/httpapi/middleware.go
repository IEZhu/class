package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"

	"github.com/IEZhu/class/backend/internal/store"
)

type ctxKey int

const userKey ctxKey = 0

func logf(format string, args ...any) { log.Printf("httpapi: "+format, args...) }

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userKey).(*store.User)
	return u
}

// requireUser — 401 без валидной сессии; кладёт пользователя в контекст запроса.
func (a *API) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "требуется вход")
			return
		}
		u, err := a.store.UserBySessionTokenHash(r.Context(), hashToken(c.Value))
		if errors.Is(err, store.ErrNotFound) {
			// сбросить битый cookie, чтобы клиент не долбился с ним до logout
			http.SetCookie(w, sessionCookieFor("", -1))
			writeError(w, http.StatusUnauthorized, "unauthorized", "сессия недействительна")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "внутренняя ошибка")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

// requireAnyRole — requireUser + 403, если роль не в списке разрешённых
// (роли: admin|teacher|student, ADR-007).
func (a *API) requireAnyRole(next http.Handler, roles ...string) http.Handler {
	return a.requireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := userFrom(r.Context())
		if u == nil || !slices.Contains(roles, u.Role) {
			writeError(w, http.StatusForbidden, "forbidden", "недостаточно прав")
			return
		}
		next.ServeHTTP(w, r)
	}))
}
