package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/IEZhu/class/backend/internal/gcal"
)

const (
	// googleCallbackPath — внешний путь callback (с префиксом /api, который
	// стрипает caddy): ровно его владелец вписывает в OAuth-клиент Google.
	googleCallbackPath = "/api/auth/google/callback"
	// googleStateCookie держит state и PKCE-verifier, пока человек на
	// экране согласия Google (ADR-023).
	googleStateCookie = "google_oauth"
	googleStateTTL    = 10 * time.Minute
)

// Итоги подключения: callback отдаёт их странице /account в ?google=.
const (
	googleConnected   = "connected"
	googleDenied      = "denied"      // человек отказался на экране согласия
	googleScopeDenied = "scope"       // согласился, но снял галочку календаря
	googleFailed      = "failed"      // state не сошёлся, код не обменялся и т. п.
	googleUnavailable = "unavailable" // ключей Google на стенде нет
)

type googleStatusResponse struct {
	// Available — ключи Google заданы, подключение возможно
	Available bool `json:"available"`
	Connected bool `json:"connected"`
}

type googleStartResponse struct {
	URL string `json:"url"`
}

// handleGoogleStatus — для /account. Подключённым считается токен, который
// расшифровывается текущим ключом: после смены GOOGLE_TOKEN_KEY страница
// предложит подключиться заново.
func (a *API) handleGoogleStatus(w http.ResponseWriter, r *http.Request) {
	resp := googleStatusResponse{Available: a.google != nil}
	if a.google != nil {
		u := userFrom(r.Context())
		sealed, err := a.store.GoogleToken(r.Context(), u.ID)
		if err != nil {
			internalError(w, "google status", err)
			return
		}
		if sealed != nil {
			_, err := a.google.Open(u.ID, sealed)
			resp.Connected = err == nil
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGoogleStart — адрес экрана согласия Google. POST, а не ссылка:
// cookie сессии SameSite=Lax не уходит с чужого сайта в POST, и запустить
// подключение за преподавателя нельзя.
func (a *API) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	if a.google == nil {
		writeError(w, http.StatusServiceUnavailable, "google_not_configured", "Google Calendar не настроен")
		return
	}
	state, _, err := newToken()
	if err != nil {
		internalError(w, "google state", err)
		return
	}
	authURL, verifier := a.google.AuthCodeURL(state)
	http.SetCookie(w, googleStateCookieFor(state+"."+verifier, int(googleStateTTL.Seconds())))
	writeJSON(w, http.StatusOK, googleStartResponse{URL: authURL})
}

// handleGoogleCallback — возврат со страницы согласия. Ответ всегда
// редирект на /account с итогом: здесь человек в браузере, а не fetch.
func (a *API) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	result := a.connectGoogle(r)
	// state одноразовый: после любого исхода нужен новый старт
	http.SetCookie(w, googleStateCookieFor("", -1))
	http.Redirect(w, r, "/account?google="+result, http.StatusSeeOther)
}

func (a *API) connectGoogle(r *http.Request) string {
	if a.google == nil {
		return googleUnavailable
	}
	q := r.URL.Query()
	// state из cookie сверяется с вернувшимся от Google: чужая ссылка
	// на callback не подключит к учётке чужой календарь
	c, err := r.Cookie(googleStateCookie)
	if err != nil {
		logf("google callback: нет cookie state (истёк или старт в другом браузере)")
		return googleFailed
	}
	state, verifier, ok := strings.Cut(c.Value, ".")
	if !ok || state == "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
		logf("google callback: state не совпал")
		return googleFailed
	}
	if q.Get("error") != "" {
		// access_denied — человек отказался; прочие коды — тоже без доступа
		logf("google callback: Google вернул %q", q.Get("error"))
		return googleDenied
	}
	u := userFrom(r.Context())
	token, err := a.google.Exchange(r.Context(), q.Get("code"), verifier)
	if errors.Is(err, gcal.ErrScopeDenied) {
		return googleScopeDenied
	}
	if err != nil {
		logf("google callback: %v", err)
		return googleFailed
	}
	sealed, err := a.google.Seal(u.ID, token)
	if err == nil {
		err = a.store.SetGoogleToken(r.Context(), u.ID, sealed)
	}
	if err != nil {
		logf("google callback: сохранение токена: %v", err)
		return googleFailed
	}
	return googleConnected
}

// handleGoogleDisconnect — токен стирается и отзывается в Google.
// Отзыв — по возможности: не дошёл — у нас токена всё равно нет, а доступ
// можно снять и в аккаунте Google. Повтор без подключения — тоже 204.
func (a *API) handleGoogleDisconnect(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	sealed, err := a.store.DisconnectGoogle(r.Context(), u.ID)
	if err != nil {
		internalError(w, "google disconnect", err)
		return
	}
	if sealed != nil && a.google != nil {
		token, err := a.google.Open(u.ID, sealed)
		if err == nil {
			err = a.google.Revoke(r.Context(), token)
		}
		if err != nil {
			logf("google disconnect: токен пользователя %d не отозван в Google: %v", u.ID, err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// googleStateCookieFor — те же защиты, что у cookie сессии. SameSite=Lax
// достаточно: возврат с Google — переход верхнего уровня, cookie с ним
// уходит. Path — только пути подключения.
func googleStateCookieFor(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     googleStateCookie,
		Value:    value,
		Path:     "/api/auth/google",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}
