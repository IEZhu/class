package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/IEZhu/class/backend/internal/gcal"
	"github.com/IEZhu/class/backend/internal/store"
)

const (
	// googleCallbackPath — внешний путь callback (с префиксом /api, который
	// стрипает caddy): ровно его владелец вписывает в OAuth-клиент Google.
	googleCallbackPath = "/api/auth/google/callback"
	// googleStateCookie держит запечатанные state и PKCE-verifier, пока
	// человек на экране согласия Google (gcal.SealFlow, ADR-023). Префикс
	// __Host- браузер принимает только с Secure, Path=/ и без Domain:
	// соседний поддомен или HTTP-страница такой cookie не подложат.
	googleStateCookie = "__Host-google_oauth"
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

type googleDisconnectResponse struct {
	// Revoked — рабочей копии нашего токена у Google не осталось: отозван
	// или его и не было. false — отозвать не вышло (токен не расшифровался,
	// Google не ответил): /account просит снять доступ в аккаунте Google
	Revoked bool `json:"revoked"`
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
	// Cookie запечатан для этого преподавателя: callback откроет его только
	// в сессии той же учётки
	sealed, err := a.google.SealFlow(userFrom(r.Context()).ID,
		gcal.Flow{State: state, Verifier: verifier, Expires: time.Now().Add(googleStateTTL)})
	if err != nil {
		internalError(w, "google state", err)
		return
	}
	http.SetCookie(w, googleStateCookieFor(sealed, int(googleStateTTL.Seconds())))
	writeJSON(w, http.StatusOK, googleStartResponse{URL: authURL})
}

// handleGoogleCallback — возврат со страницы согласия. Здесь человек
// в браузере, а не fetch, поэтому ответ хендлера — всегда редирект, и
// маршрут — без teacher(): сессию хендлер проверяет сам. Без сессии
// (истекла за время согласия, вышли в другой вкладке) — на /login,
// не преподаватель — /account?google=failed, а не JSON 401/403.
func (a *API) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	// state одноразовый: после любого исхода нужен новый старт
	http.SetCookie(w, googleStateCookieFor("", -1))
	u, err := a.sessionUser(r)
	if errors.Is(err, errNoSession) || errors.Is(err, store.ErrNotFound) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	result := googleFailed
	switch {
	case err != nil:
		logf("google callback: сессия: %v", err)
	case u.Role != store.RoleTeacher:
		logf("google callback: у пользователя %d роль %s, календарь подключает только преподаватель", u.ID, u.Role)
	default:
		result = a.connectGoogle(r, u)
	}
	http.Redirect(w, r, "/account?google="+result, http.StatusSeeOther)
}

func (a *API) connectGoogle(r *http.Request, u *store.User) string {
	if a.google == nil {
		return googleUnavailable
	}
	q := r.URL.Query()
	// state из cookie сверяется с вернувшимся от Google: чужая ссылка
	// на callback не подключит к учётке чужой календарь. Cookie открывается
	// только ключом стенда и только для этого преподавателя: подложенный
	// или начатый в другой сессии отклоняется до обмена кода
	c, err := r.Cookie(googleStateCookie)
	if err != nil {
		logf("google callback: нет cookie state (истёк или старт в другом браузере)")
		return googleFailed
	}
	flow, err := a.google.OpenFlow(u.ID, c.Value, time.Now())
	if err != nil {
		logf("google callback: cookie state пользователя %d отклонён: %v", u.ID, err)
		return googleFailed
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(flow.State)) != 1 {
		logf("google callback: state не совпал")
		return googleFailed
	}
	if q.Get("error") != "" {
		// access_denied — человек отказался; прочие коды — тоже без доступа
		logf("google callback: Google вернул %q", q.Get("error"))
		return googleDenied
	}
	token, err := a.google.Exchange(r.Context(), q.Get("code"), flow.Verifier)
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
// Отзыв — по возможности: у нас токена после стирания нет в любом случае,
// а не вышло отозвать — ответ {revoked:false}, и /account просит снять
// доступ в аккаунте Google. Повтор без подключения — {revoked:true}.
func (a *API) handleGoogleDisconnect(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	sealed, err := a.store.DisconnectGoogle(r.Context(), u.ID)
	if err != nil {
		internalError(w, "google disconnect", err)
		return
	}
	writeJSON(w, http.StatusOK, googleDisconnectResponse{Revoked: sealed == nil || a.revokeGoogle(r.Context(), u.ID, sealed)})
}

// revokeGoogle — отзыв уже стёртого токена. Контекст отвязан от запроса:
// закрытая вкладка не обрывает отзыв, потолок — таймаут клиента gcal.
func (a *API) revokeGoogle(ctx context.Context, userID int64, sealed []byte) bool {
	if a.google == nil {
		logf("google disconnect: токен пользователя %d не отозван: Google Calendar не настроен", userID)
		return false
	}
	token, err := a.google.Open(userID, sealed)
	if err == nil {
		err = a.google.Revoke(context.WithoutCancel(ctx), token)
	}
	if err != nil {
		logf("google disconnect: токен пользователя %d не отозван в Google: %v", userID, err)
		return false
	}
	return true
}

// googleStateCookieFor — те же защиты, что у cookie сессии, плюс префикс
// __Host- (Path=/, без Domain). SameSite=Lax достаточно: возврат с Google —
// переход верхнего уровня, cookie с ним уходит.
func googleStateCookieFor(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     googleStateCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}
