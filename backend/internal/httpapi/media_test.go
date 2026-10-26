package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

// GET /media/{lesson_id}/url подписывает ссылку на приватную запись урока,
// поэтому граница — та же, что у страницы урока: преподаватель и участники.
// Чужой студент, другой преподаватель, админ вне урока и гость другого
// события получают 403, без входа — 401; записи нет — 404, хранилища
// нет — 503. Подпись считается офлайн (TestPresignGetShape).
func TestMediaURLAuthorization(t *testing.T) {
	base, conn := securityDB(t)
	ctx := context.Background()
	teacherID, teacher := securityUser(t, base, conn, store.RoleTeacher, "media-teacher")
	studentID, student := securityUser(t, base, conn, store.RoleStudent, "media-student")
	_, outsider := securityUser(t, base, conn, store.RoleStudent, "media-outsider")
	_, otherTeacher := securityUser(t, base, conn, store.RoleTeacher, "media-other-teacher")
	_, admin := securityUser(t, base, conn, store.RoleAdmin, "media-admin")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('media','B1') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, groupID, studentID); err != nil {
		t.Fatal(err)
	}
	lesson, err := base.store.CreateLesson(ctx, &groupID, "", teacherID, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Гость открытого события того же преподавателя: свой урок ему виден,
	// чужой — нет
	event, err := base.store.CreateLesson(ctx, nil, "Открытый урок", teacherID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var guestID int64
	if err := conn.QueryRow(ctx, `INSERT INTO users(email, role, name) VALUES ('guest+media@guest.invalid', 'guest', 'Гость')
		RETURNING id`).Scan(&guestID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO lesson_participants(lesson_id, user_id) VALUES ($1, $2)`, event.ID, guestID); err != nil {
		t.Fatal(err)
	}
	guestToken, guestHash, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := base.store.CreateSession(ctx, guestID, guestHash, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	guest := sessionCookieFor(guestToken, 3600)

	a, err := New(base.store, Config{Storage: storage.Config{
		Bucket: "lingua-class", Endpoint: "https://acc.r2.cloudflarestorage.com",
		AccessKey: "key", SecretKey: "secret",
	}})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	path := fmt.Sprintf("/media/%d/url", lesson.ID)

	expectStatus(t, securityRequest(h, "GET", path, "", nil), http.StatusUnauthorized)
	// Записи ещё нет: участнику — 404, чужому — всё равно 403
	expectStatus(t, securityRequest(h, "GET", path, "", student), http.StatusNotFound)
	expectStatus(t, securityRequest(h, "GET", path, "", outsider), http.StatusForbidden)

	key := fmt.Sprintf("recordings/%d/20261026T100000Z-0123456789abcdef.mp4", lesson.ID)
	if _, err := conn.Exec(ctx, `UPDATE lessons SET recording_s3_key = $2, status = 'done' WHERE id = $1`, lesson.ID, key); err != nil {
		t.Fatal(err)
	}
	for name, cookie := range map[string]*http.Cookie{"студент урока": student, "преподаватель урока": teacher} {
		w := securityRequest(h, "GET", path, "", cookie)
		expectStatus(t, w, http.StatusOK)
		var body mediaURLResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(body.URL)
		if err != nil {
			t.Fatal(err)
		}
		if u.Host != "acc.r2.cloudflarestorage.com" || u.Path != "/lingua-class/"+key ||
			u.Query().Get("X-Amz-Expires") != "1800" || u.Query().Get("X-Amz-Signature") == "" {
			t.Errorf("%s: ссылка %s", name, body.URL)
		}
		if until := time.Until(body.ExpiresAt); until < 29*time.Minute || until > 31*time.Minute {
			t.Errorf("%s: expires_at через %s, хотели 30 мин", name, until)
		}
	}
	for name, cookie := range map[string]*http.Cookie{
		"чужой студент": outsider, "другой преподаватель": otherTeacher, "админ вне урока": admin,
		"гость другого события": guest,
	} {
		w := securityRequest(h, "GET", path, "", cookie)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: ответ %d, хотели 403 (%s)", name, w.Code, w.Body.String())
		}
	}
	// Своё событие гостю видно, записи у него нет
	expectStatus(t, securityRequest(h, "GET", fmt.Sprintf("/media/%d/url", event.ID), "", guest), http.StatusNotFound)
	expectStatus(t, securityRequest(h, "GET", fmt.Sprintf("/media/%d/url", lesson.ID+1000), "", student), http.StatusNotFound)
	expectStatus(t, securityRequest(h, "GET", "/media/abc/url", "", student), http.StatusBadRequest)

	// Без хранилища эндпоинт честно недоступен, а не отдаёт пустую ссылку
	expectStatus(t, securityRequest(base.Router(), "GET", path, "", student), http.StatusServiceUnavailable)
}
