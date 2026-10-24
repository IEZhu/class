package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// Every test owns a schema in a disposable test database. CI requires the
// database rather than silently skipping the authorization boundary tests.
func securityDB(t *testing.T) (*API, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL is required in CI")
		}
		t.Skip("set TEST_DATABASE_URL to a disposable Postgres database")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	schema := "security_" + token[:16]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("clean test schema: %v", err)
		}
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	files, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migration inventory: %v (%d files)", err, len(files))
	}
	sort.Strings(files)
	for _, file := range files {
		sql, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("migration %s: %v", file, err)
		}
	}
	s, err := store.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return New(s, Config{PublicBaseURL: "https://class.example", LiveKitURL: "wss://rooms.example",
		LiveKitAPIKey: "fixture-key", LiveKitAPISecret: "fixture-signing-key-at-least-32-bytes"}), conn
}

func securityUser(t *testing.T, a *API, conn *pgx.Conn, role, name string) (int64, *http.Cookie) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = conn.QueryRow(context.Background(), `INSERT INTO users(email, role, name, password_hash)
		VALUES ($1,$2,$3,$4) RETURNING id`, name+"@example.com", role, name, string(hash)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	token, tokenHash, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateSession(context.Background(), id, tokenHash, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return id, sessionCookieFor(token, 3600)
}

func securityRequest(h http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func expectStatus(t *testing.T, w *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if w.Code != expected {
		t.Fatalf("status %d, want %d; response %s", w.Code, expected, w.Body.String())
	}
}

func TestSecurityAuthorizationAndLessonIsolation(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	teacherID, teacher := securityUser(t, a, conn, store.RoleTeacher, "teacher")
	_, otherTeacher := securityUser(t, a, conn, store.RoleTeacher, "other-teacher")
	studentID, student := securityUser(t, a, conn, store.RoleStudent, "student")
	_, outsider := securityUser(t, a, conn, store.RoleStudent, "outsider")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('private','B1') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, groupID, studentID); err != nil {
		t.Fatal(err)
	}
	lesson, err := a.store.CreateLesson(ctx, groupID, teacherID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	path := fmt.Sprintf("/lessons/%d", lesson.ID)
	for _, route := range []string{"/auth/me", "/groups", "/users", "/invites", "/lessons", path, path + "/room-token"} {
		expectStatus(t, securityRequest(h, "GET", route, "", nil), http.StatusUnauthorized)
	}
	for _, cookie := range []*http.Cookie{student, teacher} {
		expectStatus(t, securityRequest(h, "GET", path, "", cookie), http.StatusOK)
		expectStatus(t, securityRequest(h, "GET", path+"/room-token", "", cookie), http.StatusOK)
	}
	for _, cookie := range []*http.Cookie{outsider, otherTeacher} {
		expectStatus(t, securityRequest(h, "GET", path, "", cookie), http.StatusForbidden)
		expectStatus(t, securityRequest(h, "GET", path+"/room-token", "", cookie), http.StatusForbidden)
		w := securityRequest(h, "GET", "/lessons", "", cookie)
		expectStatus(t, w, http.StatusOK)
		if strings.TrimSpace(w.Body.String()) != "[]" {
			t.Fatalf("unrelated account saw private lesson: %s", w.Body.String())
		}
	}
	for _, cookie := range []*http.Cookie{student, teacher} {
		expectStatus(t, securityRequest(h, "POST", "/groups", `{"name":"injected","level":"B1"}`, cookie), http.StatusForbidden)
		expectStatus(t, securityRequest(h, "POST", fmt.Sprintf("/groups/%d/members", groupID), `{"user_id":1}`, cookie), http.StatusForbidden)
	}
	expectStatus(t, securityRequest(h, "POST", "/invites", `{"email":"x@example.com","name":"X","role":"admin"}`, teacher), http.StatusForbidden)
	expectStatus(t, securityRequest(h, "POST", "/users", `{"email":"x@example.com","name":"X","role":"admin","password":"long-password"}`, teacher), http.StatusForbidden)
	expectStatus(t, securityRequest(h, "PATCH", fmt.Sprintf("/users/%d", studentID), `{"role":"admin"}`, teacher), http.StatusForbidden)
	expectStatus(t, securityRequest(h, "POST", path+"/materials", `{"title":"overwrite","body_md":"private"}`, otherTeacher), http.StatusForbidden)
	expectStatus(t, securityRequest(h, "DELETE", path, "", otherTeacher), http.StatusForbidden)
	if _, err := conn.Exec(ctx, `UPDATE lessons SET status='done' WHERE id=$1`, lesson.ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, securityRequest(h, "GET", path+"/room-token", "", teacher), http.StatusConflict)
}

func TestSecuritySessionLifecycle(t *testing.T) {
	a, conn := securityDB(t)
	userID, oldCookie := securityUser(t, a, conn, store.RoleStudent, "session-user")
	h := a.Router()
	expectStatus(t, securityRequest(h, "GET", "/auth/me", "", &http.Cookie{Name: sessionCookie, Value: "forged"}), http.StatusUnauthorized)
	w := securityRequest(h, "POST", "/auth/login", `{"email":"session-user@example.com","password":"fixture-password"}`, nil)
	expectStatus(t, w, http.StatusOK)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie lost its transport protections")
	}
	current := cookies[0]
	var stored string
	if err := conn.QueryRow(context.Background(), `SELECT token_hash FROM sessions WHERE token_hash=$1`, hashToken(current.Value)).Scan(&stored); err != nil || stored == current.Value {
		t.Fatalf("session must persist only a digest: %v", err)
	}
	expectStatus(t, securityRequest(h, "POST", "/auth/password", `{"current_password":"wrong","new_password":"changed-password"}`, current), http.StatusForbidden)
	w = securityRequest(h, "POST", "/auth/password", `{"current_password":"fixture-password","new_password":"changed-password"}`, current)
	expectStatus(t, w, http.StatusNoContent)
	expectStatus(t, securityRequest(h, "GET", "/auth/me", "", oldCookie), http.StatusUnauthorized)
	expectStatus(t, securityRequest(h, "GET", "/auth/me", "", current), http.StatusUnauthorized)
	current = w.Result().Cookies()[0]
	expectStatus(t, securityRequest(h, "GET", "/auth/me", "", current), http.StatusOK)
	expectStatus(t, securityRequest(h, "POST", "/auth/logout", "", current), http.StatusNoContent)
	expectStatus(t, securityRequest(h, "GET", "/auth/me", "", current), http.StatusUnauthorized)
	if err := a.store.CreateSession(context.Background(), userID, hashToken("expired"), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, securityRequest(h, "GET", "/auth/me", "", &http.Cookie{Name: sessionCookie, Value: "expired"}), http.StatusUnauthorized)
	for _, body := range []string{`{"email":"missing@example.com","password":"wrong"}`, `{"email":"' OR 1=1 --","password":"wrong"}`} {
		expectStatus(t, securityRequest(h, "POST", "/auth/login", body, nil), http.StatusUnauthorized)
	}
}

func TestSecurityInvitesExpireAndAcceptExactlyOnce(t *testing.T) {
	a, conn := securityDB(t)
	adminID, _ := securityUser(t, a, conn, store.RoleAdmin, "inviter")
	_, otherTeacher := securityUser(t, a, conn, store.RoleTeacher, "other-inviter")
	ctx := context.Background()
	for _, item := range []struct {
		token   string
		expires time.Time
		status  int
	}{
		{"expired", time.Now().Add(-time.Hour), http.StatusGone},
		{"valid", time.Now().Add(time.Hour), http.StatusOK},
	} {
		invite, err := a.store.CreateInvite(ctx, hashToken(item.token), item.token+"@example.com", "Invited", store.RoleStudent, nil, adminID, item.expires)
		if err != nil {
			t.Fatal(err)
		}
		h := a.Router()
		expectStatus(t, securityRequest(h, "DELETE", fmt.Sprintf("/invites/%d", invite.ID), "", otherTeacher), http.StatusNotFound)
		expectStatus(t, securityRequest(h, "GET", "/signup/"+item.token, "", nil), item.status)
		if item.status == http.StatusGone {
			expectStatus(t, securityRequest(h, "POST", "/signup/"+item.token, `{"password":"new-password"}`, nil), http.StatusGone)
		}
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Go(func() {
			results <- securityRequest(a.Router(), "POST", "/signup/valid", `{"password":"new-password"}`, nil)
		})
	}
	wg.Wait()
	close(results)
	statuses := []int{}
	for w := range results {
		statuses = append(statuses, w.Code)
		if w.Code == http.StatusCreated {
			var user userResponse
			if err := json.Unmarshal(w.Body.Bytes(), &user); err != nil || user.Role != store.RoleStudent {
				t.Fatalf("invite role must be server-controlled: %v", err)
			}
			expectStatus(t, securityRequest(a.Router(), "GET", "/auth/me", "", w.Result().Cookies()[0]), http.StatusOK)
		}
	}
	sort.Ints(statuses)
	if fmt.Sprint(statuses) != "[201 410]" {
		t.Fatalf("one invite must create exactly one account: %v", statuses)
	}
	expectStatus(t, securityRequest(a.Router(), "GET", "/signup/valid", "", nil), http.StatusGone)
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM users WHERE email='valid@example.com'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("single-use invite created %d accounts: %v", count, err)
	}
}
