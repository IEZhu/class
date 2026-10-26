package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/IEZhu/class/backend/internal/gcal"
	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/worker"
)

const (
	fakeAuthCode     = "fixture-auth-code"
	fakeAccessToken  = "fixture-access-token"
	fakeRefreshToken = "1//fixture-refresh-token"
)

// fakeGoogle — httptest-двойник OAuth- и Calendar-эндпоинтов Google:
// настоящих вызовов тесты не делают.
type fakeGoogle struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// challenge — code_challenge последнего старта: обмен кода без
	// подходящего verifier отклоняется, как у Google
	challenge  string
	scope      string // scope в ответе обмена кода
	revoked    bool   // обновление refresh token → invalid_grant
	failNext   int    // код ответа следующего вызова Calendar API
	revokeFail int    // код ответа отзыва вместо 200
	exchanges  int
	calls      []string // "METHOD id" вызовов Calendar API
	events     map[string]fakeEvent
	deleted    map[string]bool
	revokes    []string
	// Хуки под мьютексом фейка: событие посреди вызова worker'а или api.
	// Тест в это время ждёт ответа, поэтому conn в хуке свободен
	onInsert  func(id string) // после создания события
	onRefresh func()          // до ответа на обновление access token
	onRevoke  func()          // до ответа на отзыв
}

type fakeEvent struct {
	ID          string `json:"id"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Start       struct {
		DateTime time.Time `json:"dateTime"`
	} `json:"start"`
	End struct {
		DateTime time.Time `json:"dateTime"`
	} `json:"end"`
	Attendees []struct {
		Email string `json:"email"`
	} `json:"attendees"`
	Status string `json:"status"`
}

func (e fakeEvent) attendees() []string {
	out := []string{}
	for _, a := range e.Attendees {
		out = append(out, a.Email)
	}
	return out
}

// withFakeGoogle подключает к api клиент Google, смотрящий в фейк.
func withFakeGoogle(t *testing.T, a *API) *fakeGoogle {
	t.Helper()
	f := &fakeGoogle{t: t, scope: gcal.Scope, events: map[string]fakeEvent{}, deleted: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("POST /revoke", f.revoke)
	mux.HandleFunc("/calendar/v3/calendars/primary/events", f.calendar)
	mux.HandleFunc("/calendar/v3/calendars/primary/events/{id}", f.calendar)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	client, err := gcal.New(fakeGoogleConfig(f.srv.URL, 1))
	if err != nil || client == nil {
		t.Fatalf("gcal.New = %v, %v", client, err)
	}
	a.google = client
	return f
}

func fakeGoogleConfig(base string, keyByte byte) gcal.Config {
	return gcal.Config{
		ClientID: "fixture-client", ClientSecret: "fixture-secret",
		TokenKey:    base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{keyByte}, 32)),
		RedirectURL: "https://class.example" + googleCallbackPath,
		AuthURL:     base + "/o/oauth2/v2/auth", TokenURL: base + "/token",
		RevokeURL: base + "/revoke", CalendarURL: base + "/calendar/v3",
	}
}

func (f *fakeGoogle) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.FormValue("client_id") != "fixture-client" || r.FormValue("client_secret") != "fixture-secret" {
		f.oauthError(w, "invalid_client")
		return
	}
	switch r.FormValue("grant_type") {
	case "authorization_code":
		f.exchanges++
		if r.FormValue("code") != fakeAuthCode ||
			r.FormValue("redirect_uri") != "https://class.example"+googleCallbackPath ||
			oauth2.S256ChallengeFromVerifier(r.FormValue("code_verifier")) != f.challenge {
			f.oauthError(w, "invalid_grant")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": fakeAccessToken, "token_type": "Bearer",
			"expires_in": 3599, "refresh_token": fakeRefreshToken, "scope": f.scope})
	case "refresh_token":
		if f.onRefresh != nil {
			f.onRefresh()
		}
		if f.revoked || r.FormValue("refresh_token") != fakeRefreshToken {
			f.oauthError(w, "invalid_grant")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": fakeAccessToken, "token_type": "Bearer", "expires_in": 3599})
	default:
		f.oauthError(w, "unsupported_grant_type")
	}
}

func (f *fakeGoogle) oauthError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

func (f *fakeGoogle) revoke(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onRevoke != nil {
		f.onRevoke()
	}
	if f.revokeFail != 0 {
		http.Error(w, "backendError", f.revokeFail)
		return
	}
	f.revokes = append(f.revokes, r.FormValue("token"))
}

func (f *fakeGoogle) calendar(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := r.PathValue("id")
	if r.Header.Get("Authorization") != "Bearer "+fakeAccessToken {
		http.Error(w, "authError", http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("sendUpdates") != "all" {
		f.t.Errorf("%s %s without sendUpdates=all", r.Method, r.URL)
	}
	var ev fakeEvent
	if r.Method != http.MethodDelete {
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id = ev.ID
	}
	f.calls = append(f.calls, r.Method+" "+id)
	if f.failNext != 0 {
		http.Error(w, "backendError", f.failNext)
		f.failNext = 0
		return
	}
	_, exists := f.events[id]
	switch {
	case r.Method == http.MethodPost && (exists || f.deleted[id]):
		http.Error(w, "duplicate", http.StatusConflict)
	case r.Method == http.MethodPost, r.Method == http.MethodPut && (exists || f.deleted[id]):
		f.events[id] = ev
		delete(f.deleted, id)
		if r.Method == http.MethodPost && f.onInsert != nil {
			f.onInsert(id)
		}
		writeJSON(w, http.StatusOK, ev)
	case r.Method == http.MethodDelete && exists:
		delete(f.events, id)
		f.deleted[id] = true
		w.WriteHeader(http.StatusNoContent)
	case f.deleted[id]:
		http.Error(w, "deleted", http.StatusGone)
	default:
		http.Error(w, "notFound", http.StatusNotFound)
	}
}

// with — доступ теста к состоянию фейка под тем же мьютексом, что у
// его хендлеров.
func (f *fakeGoogle) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// takeCalls — вызовы Calendar API с прошлого takeCalls.
func (f *fakeGoogle) takeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.calls
	f.calls = nil
	return calls
}

// startGoogle — POST /auth/google/start: cookie со state и адрес согласия.
func startGoogle(t *testing.T, f *fakeGoogle, h http.Handler, cookie *http.Cookie) (*http.Cookie, url.Values) {
	t.Helper()
	w := securityRequest(h, "POST", "/auth/google/start", "", cookie)
	expectStatus(t, w, http.StatusOK)
	var resp googleStartResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(resp.URL)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.challenge = u.Query().Get("code_challenge")
	f.mu.Unlock()
	for _, c := range w.Result().Cookies() {
		if c.Name == googleStateCookie {
			return c, u.Query()
		}
	}
	t.Fatal("start set no state cookie")
	return nil, nil
}

// callbackRequest — возврат браузера с Google: cookie сессии и state.
func callbackRequest(h http.Handler, query url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/auth/google/callback?"+query.Encode(), nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// clearsStateCookie — ответ гасит одноразовый cookie подключения; для
// префикса __Host- браузер примет это только с Secure и Path=/.
func clearsStateCookie(w *httptest.ResponseRecorder) bool {
	for _, c := range w.Result().Cookies() {
		if c.Name == googleStateCookie && c.MaxAge < 0 && c.Secure && c.Path == "/" && c.Domain == "" {
			return true
		}
	}
	return false
}

// googleCallback — callback в сессии: итог из редиректа на /account.
func googleCallback(t *testing.T, h http.Handler, query url.Values, cookies ...*http.Cookie) string {
	t.Helper()
	w := callbackRequest(h, query, cookies...)
	expectStatus(t, w, http.StatusSeeOther)
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || loc.Path != "/account" {
		t.Fatalf("callback redirected to %q", w.Header().Get("Location"))
	}
	if !clearsStateCookie(w) {
		t.Fatal("callback must clear the one-time state cookie")
	}
	return loc.Query().Get("google")
}

// connectTeacherGoogle — полный проход подключения до «connected».
func connectTeacherGoogle(t *testing.T, f *fakeGoogle, h http.Handler, teacher *http.Cookie) {
	t.Helper()
	state, q := startGoogle(t, f, h, teacher)
	if got := googleCallback(t, h, url.Values{"state": {q.Get("state")}, "code": {fakeAuthCode}}, teacher, state); got != googleConnected {
		t.Fatalf("connect result %q", got)
	}
}

// googleDisconnect — DELETE /auth/google; true — токен отозван в Google
// или его и не было.
func googleDisconnect(t *testing.T, h http.Handler, cookie *http.Cookie) bool {
	t.Helper()
	w := securityRequest(h, "DELETE", "/auth/google", "", cookie)
	expectStatus(t, w, http.StatusOK)
	var resp googleDisconnectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Revoked
}

func googleStatus(t *testing.T, h http.Handler, cookie *http.Cookie) googleStatusResponse {
	t.Helper()
	w := securityRequest(h, "GET", "/auth/google", "", cookie)
	expectStatus(t, w, http.StatusOK)
	var resp googleStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func storedGoogleToken(t *testing.T, conn *pgx.Conn, userID int64) []byte {
	t.Helper()
	var sealed []byte
	if err := conn.QueryRow(context.Background(), `SELECT google_refresh_token FROM users WHERE id = $1`, userID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	return sealed
}

func calendarJobs(t *testing.T, conn *pgx.Conn) []store.CalendarSync {
	t.Helper()
	rows, err := conn.Query(context.Background(),
		`SELECT payload FROM jobs WHERE kind = $1 AND failed_at IS NULL ORDER BY id`, store.JobCalendarSync)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.CalendarSync, error) {
		var p store.CalendarSync
		err := row.Scan(&p)
		return p, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// captureLog — журнал на время теста: строки о пропуске урока — часть
// контракта (интеграция без ключей «пропускается со строкой в журнале»).
func captureLog(t *testing.T) *lockedBuffer {
	buf := &lockedBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestGoogleConnectStoresOnlyEncryptedToken(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	f := withFakeGoogle(t, a)
	h := a.Router()
	teacherID, teacher := securityUser(t, a, conn, store.RoleTeacher, "teacher")
	studentID, student := securityUser(t, a, conn, store.RoleStudent, "student")
	_, admin := securityUser(t, a, conn, store.RoleAdmin, "admin")

	// Календарь — только у преподавателя: уроки создаёт он один. Callback
	// проверяет сессию сам (TestGoogleCallbackBoundToSession)
	for _, cookie := range []*http.Cookie{student, admin} {
		expectStatus(t, securityRequest(h, "GET", "/auth/google", "", cookie), http.StatusForbidden)
		expectStatus(t, securityRequest(h, "POST", "/auth/google/start", "", cookie), http.StatusForbidden)
		expectStatus(t, securityRequest(h, "DELETE", "/auth/google", "", cookie), http.StatusForbidden)
	}
	for _, route := range []string{"GET /auth/google", "POST /auth/google/start", "DELETE /auth/google"} {
		method, path, _ := strings.Cut(route, " ")
		expectStatus(t, securityRequest(h, method, path, "", nil), http.StatusUnauthorized)
	}
	if got := googleStatus(t, h, teacher); got != (googleStatusResponse{Available: true}) {
		t.Fatalf("status before connect %+v", got)
	}

	// Урок до подключения: джобы нет, ставит её само подключение
	future, err := a.store.CreateLesson(ctx, nil, "future", teacherID, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.CreateLesson(ctx, nil, "past", teacherID, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := calendarJobs(t, conn); len(got) != 0 {
		t.Fatalf("jobs for a teacher without Google: %v", got)
	}

	state, q := startGoogle(t, f, h, teacher)
	flow, err := a.google.OpenFlow(teacherID, state.Value, time.Now())
	if err != nil {
		t.Fatalf("state cookie does not open for its teacher: %v", err)
	}
	stateValue := flow.State
	if until := time.Until(flow.Expires); until <= googleStateTTL-time.Minute || until > googleStateTTL {
		t.Fatalf("flow expires in %s, want %s", until, googleStateTTL)
	}
	if state.Name != "__Host-google_oauth" || !state.HttpOnly || !state.Secure || state.SameSite != http.SameSiteLaxMode ||
		state.Path != "/" || state.Domain != "" || state.MaxAge != int(googleStateTTL.Seconds()) ||
		q.Get("state") != stateValue || q.Get("code_challenge") != oauth2.S256ChallengeFromVerifier(flow.Verifier) ||
		q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("scope") != gcal.Scope ||
		q.Get("redirect_uri") != "https://class.example/api/auth/google/callback" {
		t.Fatalf("start: cookie %+v, auth params %v", state, q)
	}

	good := url.Values{"state": {stateValue}, "code": {fakeAuthCode}}
	forgedValue, err := a.google.SealFlow(teacherID, gcal.Flow{State: stateValue,
		Verifier: oauth2.GenerateVerifier(), Expires: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	forged := &http.Cookie{Name: googleStateCookie, Value: forgedValue}
	for _, attempt := range []struct {
		query   url.Values
		cookies []*http.Cookie
		want    string
	}{
		{url.Values{"state": {"attacker-state"}, "code": {fakeAuthCode}}, []*http.Cookie{teacher, state}, googleFailed},
		{good, []*http.Cookie{teacher}, googleFailed}, // cookie state истёк
		{url.Values{"state": {""}, "code": {fakeAuthCode}}, []*http.Cookie{teacher, {Name: googleStateCookie, Value: "."}}, googleFailed},
		{url.Values{"state": {stateValue}, "error": {"access_denied"}}, []*http.Cookie{teacher, state}, googleDenied},
	} {
		if got := googleCallback(t, h, attempt.query, attempt.cookies...); got != attempt.want {
			t.Fatalf("callback %v: %q, want %q", attempt.query, got, attempt.want)
		}
	}
	f.with(func() {
		if f.exchanges != 0 {
			t.Fatalf("rejected callbacks reached the token endpoint %d times", f.exchanges)
		}
	})
	// Чужой verifier при верном state — Google не обменяет код (PKCE)
	if got := googleCallback(t, h, good, teacher, forged); got != googleFailed {
		t.Fatalf("forged verifier: %q", got)
	}
	f.with(func() {
		if f.exchanges != 1 {
			t.Fatalf("PKCE case must reach the token endpoint once, got %d", f.exchanges)
		}
	})
	f.with(func() { f.scope = "openid" })
	if got := googleCallback(t, h, good, teacher, state); got != googleScopeDenied {
		t.Fatalf("scope denied: %q", got)
	}
	f.with(func() { f.scope = gcal.Scope })
	if sealed := storedGoogleToken(t, conn, teacherID); sealed != nil {
		t.Fatalf("failed attempts stored a token: %x", sealed)
	}

	if got := googleCallback(t, h, good, teacher, state); got != googleConnected {
		t.Fatalf("connect: %q", got)
	}

	sealed := storedGoogleToken(t, conn, teacherID)
	var plainInColumn bool
	if err := conn.QueryRow(ctx,
		`SELECT position(convert_to($2, 'UTF8') IN google_refresh_token) > 0 FROM users WHERE id = $1`,
		teacherID, fakeRefreshToken).Scan(&plainInColumn); err != nil {
		t.Fatal(err)
	}
	if len(sealed) == 0 || plainInColumn || bytes.Contains(sealed, []byte(fakeRefreshToken)) {
		t.Fatalf("column holds the plaintext token: %x", sealed)
	}
	if token, err := a.google.Open(teacherID, sealed); err != nil || token != fakeRefreshToken {
		t.Fatalf("configured key opens %q, %v", token, err)
	}
	if _, err := a.google.Open(studentID, sealed); err == nil {
		t.Fatal("ciphertext must be bound to its users row")
	}
	otherKey, err := gcal.New(fakeGoogleConfig(f.srv.URL, 2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherKey.Open(teacherID, sealed); err == nil {
		t.Fatal("another key opened the token")
	}
	if got := googleStatus(t, h, teacher); got != (googleStatusResponse{Available: true, Connected: true}) {
		t.Fatalf("status after connect %+v", got)
	}
	// Подключение поставило будущий урок, прошедший — нет
	if got := calendarJobs(t, conn); len(got) != 1 || got[0] != (store.CalendarSync{LessonID: future.ID}) {
		t.Fatalf("backfill jobs %v, want lesson %d", got, future.ID)
	}

	// Сменили ключ — токен нечитаем, страница предлагает подключиться заново
	a.google = otherKey
	if got := googleStatus(t, h, teacher); got.Connected {
		t.Fatal("undecryptable token reported as connected")
	}
	a.google, _ = gcal.New(fakeGoogleConfig(f.srv.URL, 1))

	if !googleDisconnect(t, h, teacher) {
		t.Fatal("disconnect did not report the revoke")
	}
	if sealed := storedGoogleToken(t, conn, teacherID); sealed != nil {
		t.Fatal("disconnect left the token")
	}
	if !googleDisconnect(t, h, teacher) {
		t.Fatal("repeated disconnect: nothing left at Google, want revoked=true")
	}
	f.with(func() {
		if !slices.Equal(f.revokes, []string{fakeRefreshToken}) {
			t.Fatalf("revoked at Google: %v", f.revokes)
		}
	})
	if got := googleStatus(t, h, teacher); got.Connected {
		t.Fatal("status after disconnect still connected")
	}
}

// Без ключей Google стенд работает: страница видит «недоступно»,
// подключение — 503, уроки создаются без джоб, а случайная джоба
// создания снимается worker'ом со строкой в журнале (удаление — ждёт).
func TestGoogleUnavailableWithoutKeys(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	if a.GoogleConfigured() {
		t.Fatal("harness api must start without Google keys")
	}
	h := a.Router()
	teacherID, teacher := securityUser(t, a, conn, store.RoleTeacher, "teacher")

	if got := googleStatus(t, h, teacher); got != (googleStatusResponse{}) {
		t.Fatalf("status %+v", got)
	}
	expectStatus(t, securityRequest(h, "POST", "/auth/google/start", "", teacher), http.StatusServiceUnavailable)
	if got := googleCallback(t, h, url.Values{"state": {"x"}, "code": {fakeAuthCode}}, teacher); got != googleUnavailable {
		t.Fatalf("callback %q", got)
	}
	if !googleDisconnect(t, h, teacher) {
		t.Fatal("disconnect without a token: want revoked=true")
	}
	// Токен остался с тех пор, когда ключи были: стереть — да, отозвать — нечем
	if _, err := conn.Exec(ctx, `UPDATE users SET google_refresh_token = '\x01' WHERE id = $1`, teacherID); err != nil {
		t.Fatal(err)
	}
	if googleDisconnect(t, h, teacher) || storedGoogleToken(t, conn, teacherID) != nil {
		t.Fatal("disconnect without keys must wipe the token and report revoked=false")
	}

	lesson, err := a.store.CreateLesson(ctx, nil, "event", teacherID, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := calendarJobs(t, conn); len(got) != 0 {
		t.Fatalf("jobs without Google: %v", got)
	}

	logs := captureLog(t)
	runner := jobs.NewRunner(a.store.Pool(), jobs.Config{WorkerID: "test-worker"})
	worker.RegisterCalendar(runner, a.store, nil, "https://class.example")
	if _, err := jobs.Enqueue(ctx, a.store.Pool(), store.JobCalendarSync, store.CalendarSync{LessonID: lesson.ID}); err != nil {
		t.Fatal(err)
	}
	if worked, err := runner.RunOnce(ctx); err != nil || !worked {
		t.Fatalf("RunOnce = %v, %v", worked, err)
	}
	var left int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("jobs left %d, %v", left, err)
	}
	if !strings.Contains(logs.String(), "Google Calendar не настроен") {
		t.Fatalf("no log line about the skipped lesson: %s", logs.String())
	}
	// Удаление события без ключей не выбрасывается: ждёт в очереди
	if _, err := jobs.Enqueue(ctx, a.store.Pool(), store.JobCalendarSync,
		store.CalendarSync{LessonID: lesson.ID, TeacherID: teacherID, EventID: "fixtureevent"}); err != nil {
		t.Fatal(err)
	}
	if worked, err := runner.RunOnce(ctx); err != nil || !worked {
		t.Fatalf("RunOnce = %v, %v", worked, err)
	}
	var lastError string
	if err := conn.QueryRow(ctx, `SELECT last_error FROM jobs WHERE failed_at IS NULL AND run_at > now()`).Scan(&lastError); err != nil ||
		!strings.Contains(lastError, "ждёт повторного подключения календаря: Google Calendar не настроен") {
		t.Fatalf("delete without keys: last_error %q, %v", lastError, err)
	}
}

// Миграция 0012 откатывается и накатывается снова: колонка и джобы
// calendar_sync уходят вместе.
func TestMigration0012Reversible(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	if _, err := jobs.Enqueue(ctx, a.store.Pool(), store.JobCalendarSync, store.CalendarSync{LessonID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Enqueue(ctx, a.store.Pool(), store.JobRecordingReady, store.RecordingReady{LessonID: 1}); err != nil {
		t.Fatal(err)
	}
	column := func() bool {
		var ok bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'users' AND column_name = 'google_refresh_token')`).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for _, step := range []struct {
		file   string
		column bool
	}{{"down", false}, {"up", true}} {
		sql, err := os.ReadFile("../../migrations/0012_google_calendar." + step.file + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("0012 %s: %v", step.file, err)
		}
		if column() != step.column {
			t.Fatalf("after 0012 %s google_refresh_token exists = %v", step.file, !step.column)
		}
	}
	var kinds []string
	rows, err := conn.Query(ctx, `SELECT kind FROM jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	if kinds, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kinds, []string{store.JobRecordingReady}) {
		t.Fatalf("jobs after rollback: %v", kinds)
	}
}

// Cookie подключения привязан к учётке, начавшей подключение: подложенный
// (и в прежнем открытом формате state.verifier, и запечатанный чужим
// ключом), начатый в другой сессии и истёкший отклоняются до обмена кода.
// Callback без сессии преподавателя отвечает редиректом, а не JSON.
func TestGoogleCallbackBoundToSession(t *testing.T) {
	a, conn := securityDB(t)
	f := withFakeGoogle(t, a)
	h := a.Router()
	victimID, victim := securityUser(t, a, conn, store.RoleTeacher, "victim")
	otherID, other := securityUser(t, a, conn, store.RoleTeacher, "other")
	_, student := securityUser(t, a, conn, store.RoleStudent, "student")
	exchanges := func() int {
		var n int
		f.with(func() { n = f.exchanges })
		return n
	}

	// Атакующий сам начал авторизацию со своими state и verifier
	// и согласился своим аккаунтом Google: код у него на руках
	verifier := oauth2.GenerateVerifier()
	f.with(func() { f.challenge = oauth2.S256ChallengeFromVerifier(verifier) })
	attack := url.Values{"state": {"attackerstate"}, "code": {fakeAuthCode}}
	attackerKey, err := gcal.New(fakeGoogleConfig(f.srv.URL, 2))
	if err != nil {
		t.Fatal(err)
	}
	sealedByAttacker, err := attackerKey.SealFlow(victimID,
		gcal.Flow{State: "attackerstate", Verifier: verifier, Expires: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"attackerstate." + verifier, sealedByAttacker} {
		if got := googleCallback(t, h, attack, victim, &http.Cookie{Name: googleStateCookie, Value: value}); got != googleFailed {
			t.Fatalf("planted cookie %q: %q", value, got)
		}
	}
	// Старт в сессии одного преподавателя не завершается в сессии другого
	theirCookie, q := startGoogle(t, f, h, other)
	theirs := url.Values{"state": {q.Get("state")}, "code": {fakeAuthCode}}
	if got := googleCallback(t, h, theirs, victim, theirCookie); got != googleFailed {
		t.Fatalf("cross-session callback: %q", got)
	}
	expired, err := a.google.SealFlow(victimID, gcal.Flow{State: "fixturestate", Verifier: verifier, Expires: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if got := googleCallback(t, h, url.Values{"state": {"fixturestate"}, "code": {fakeAuthCode}}, victim,
		&http.Cookie{Name: googleStateCookie, Value: expired}); got != googleFailed {
		t.Fatalf("expired flow: %q", got)
	}

	// Без сессии — на вход, не преподаватель — failed; cookie гасится всегда
	for _, tc := range []struct {
		name    string
		cookies []*http.Cookie
		want    string
	}{
		{"no session", []*http.Cookie{theirCookie}, "/login"},
		{"stale session", []*http.Cookie{{Name: sessionCookie, Value: "expired"}, theirCookie}, "/login"},
		{"student", []*http.Cookie{student, theirCookie}, "/account?google=failed"},
	} {
		w := callbackRequest(h, theirs, tc.cookies...)
		expectStatus(t, w, http.StatusSeeOther)
		if got := w.Header().Get("Location"); got != tc.want || !clearsStateCookie(w) {
			t.Fatalf("%s: redirect %q (want %q), state cookie cleared %v", tc.name, got, tc.want, clearsStateCookie(w))
		}
	}
	if n := exchanges(); n != 0 {
		t.Fatalf("refused callbacks reached the token endpoint %d times", n)
	}
	if storedGoogleToken(t, conn, victimID) != nil || storedGoogleToken(t, conn, otherID) != nil {
		t.Fatal("a refused callback stored a token")
	}

	// Свой старт в своей сессии по-прежнему подключает
	if got := googleCallback(t, h, theirs, other, theirCookie); got != googleConnected || storedGoogleToken(t, conn, otherID) == nil {
		t.Fatalf("own callback: %q", got)
	}
}

// Отключение: отзыв не обрывается вместе с запросом браузера, а не вышло
// отозвать — {revoked:false}; токен у нас стёрт в любом случае.
func TestGoogleDisconnectReportsRevoke(t *testing.T) {
	a, conn := securityDB(t)
	f := withFakeGoogle(t, a)
	h := a.Router()
	teacherID, teacher := securityUser(t, a, conn, store.RoleTeacher, "teacher")
	revokes := func() int {
		var n int
		f.with(func() { n = len(f.revokes) })
		return n
	}

	// Вкладку закрыли, пока шёл отзыв: контекст запроса отменён
	connectTeacherGoogle(t, f, h, teacher)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.with(func() {
		f.onRevoke = func() {
			cancel()
			// клиент успевает увидеть отмену раньше ответа
			time.Sleep(100 * time.Millisecond)
		}
	})
	r := httptest.NewRequest("DELETE", "/auth/google", nil).WithContext(ctx)
	r.AddCookie(teacher)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	expectStatus(t, w, http.StatusOK)
	if strings.TrimSpace(w.Body.String()) != `{"revoked":true}` || revokes() != 1 {
		t.Fatalf("disconnect with a cancelled request: %s, revokes %d", w.Body.String(), revokes())
	}
	f.with(func() { f.onRevoke = nil })

	// Google не принял отзыв
	connectTeacherGoogle(t, f, h, teacher)
	f.with(func() { f.revokeFail = http.StatusServiceUnavailable })
	if googleDisconnect(t, h, teacher) || storedGoogleToken(t, conn, teacherID) != nil {
		t.Fatal("failed revoke: want revoked=false and the token wiped")
	}
	f.with(func() { f.revokeFail = 0 })

	// Сменили GOOGLE_TOKEN_KEY: токен не расшифровать, отозвать нечем
	connectTeacherGoogle(t, f, h, teacher)
	before := revokes()
	otherKey, err := gcal.New(fakeGoogleConfig(f.srv.URL, 2))
	if err != nil {
		t.Fatal(err)
	}
	a.google = otherKey
	if googleDisconnect(t, h, teacher) || storedGoogleToken(t, conn, teacherID) != nil || revokes() != before {
		t.Fatal("undecryptable token: want revoked=false, the token wiped and no revoke call")
	}
}
