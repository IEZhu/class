package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
	"github.com/jackc/pgx/v5"
)

// Тесты гостевого доступа (S1-11, ADR-010) на той же изолированной схеме,
// что и security_test: настоящие миграции, настоящий роутер.

const guestLinkPrefix = "https://class.example/join/"

// openEvent — событие без группы, которое ещё не закончилось.
func openEvent(t *testing.T, a *API, teacherID int64, title string) *store.Lesson {
	t.Helper()
	l, err := a.store.CreateLesson(context.Background(), nil, title, teacherID,
		time.Now().Add(-10*time.Minute), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// issueGuestLink выпускает ссылку через api и возвращает её токен.
func issueGuestLink(t *testing.T, h http.Handler, lessonID int64, cookie *http.Cookie) string {
	t.Helper()
	w := securityRequest(h, "POST", fmt.Sprintf("/lessons/%d/guest-link", lessonID), "", cookie)
	expectStatus(t, w, http.StatusCreated)
	var resp guestLinkResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	token, ok := strings.CutPrefix(resp.URL, guestLinkPrefix)
	if !ok || token == "" {
		t.Fatalf("ссылка не на /join/{token}: %q", resp.URL)
	}
	return token
}

func TestGuestLinkIssueRevokeReissueAuthorization(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	organizerID, organizer := securityUser(t, a, conn, store.RoleTeacher, "organizer")
	_, otherTeacher := securityUser(t, a, conn, store.RoleTeacher, "other-organizer")
	_, student := securityUser(t, a, conn, store.RoleStudent, "link-student")
	_, admin := securityUser(t, a, conn, store.RoleAdmin, "link-admin")
	event := openEvent(t, a, organizerID, "Разговорный клуб")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('B1 вечер','B1') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	groupLesson, err := a.store.CreateLesson(ctx, &groupID, "", organizerID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	path := fmt.Sprintf("/lessons/%d/guest-link", event.ID)

	for _, method := range []string{"POST", "DELETE"} {
		expectStatus(t, securityRequest(h, method, path, "", nil), http.StatusUnauthorized)
		// student не staff — отсекает middleware; чужой преподаватель — lessonForOwner
		expectStatus(t, securityRequest(h, method, path, "", student), http.StatusForbidden)
		expectStatus(t, securityRequest(h, method, path, "", otherTeacher), http.StatusForbidden)
		// У урока с группой гостевой ссылки нет: иначе в него зашли бы посторонние
		expectStatus(t, securityRequest(h, method, fmt.Sprintf("/lessons/%d/guest-link", groupLesson.ID), "", organizer), http.StatusConflict)
		expectStatus(t, securityRequest(h, method, "/lessons/999999/guest-link", "", organizer), http.StatusNotFound)
	}

	first := issueGuestLink(t, h, event.ID, organizer)
	expectStatus(t, securityRequest(h, "GET", "/join/"+first, "", nil), http.StatusOK)
	var stored string
	if err := conn.QueryRow(ctx, `SELECT guest_token_hash FROM lessons WHERE id=$1`, event.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != hashToken(first) {
		t.Fatal("в БД должен лежать только sha256-дайджест гостевого токена")
	}

	// Перевыпуск (здесь — админом) отключает предыдущую ссылку сразу
	second := issueGuestLink(t, h, event.ID, admin)
	if second == first {
		t.Fatal("перевыпуск вернул тот же токен")
	}
	expectStatus(t, securityRequest(h, "GET", "/join/"+first, "", nil), http.StatusNotFound)
	expectStatus(t, securityRequest(h, "POST", "/join/"+first, `{"name":"Старая ссылка"}`, nil), http.StatusNotFound)
	expectStatus(t, securityRequest(h, "GET", "/join/"+second, "", nil), http.StatusOK)

	expectStatus(t, securityRequest(h, "DELETE", path, "", organizer), http.StatusNoContent)
	expectStatus(t, securityRequest(h, "GET", "/join/"+second, "", nil), http.StatusNotFound)
	expectStatus(t, securityRequest(h, "POST", "/join/"+second, `{"name":"Отозванная"}`, nil), http.StatusNotFound)
	// Отзыв идемпотентен
	expectStatus(t, securityRequest(h, "DELETE", path, "", admin), http.StatusNoContent)

	var guests int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='guest'`).Scan(&guests); err != nil || guests != 0 {
		t.Fatalf("по мёртвым ссылкам завелись гости: %d (%v)", guests, err)
	}
}

func TestGuestJoinStatuses(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	organizerID, organizer := securityUser(t, a, conn, store.RoleTeacher, "join-organizer")
	event := openEvent(t, a, organizerID, "Открытый урок")
	h := a.Router()
	token := issueGuestLink(t, h, event.ID, organizer)

	expectStatus(t, securityRequest(h, "GET", "/join/unknown-token", "", nil), http.StatusNotFound)
	expectStatus(t, securityRequest(h, "POST", "/join/unknown-token", `{"name":"Гость"}`, nil), http.StatusNotFound)

	w := securityRequest(h, "GET", "/join/"+token, "", nil)
	expectStatus(t, w, http.StatusOK)
	var preview guestEventResponse
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Title != "Открытый урок" || preview.TeacherName != "join-organizer" || !preview.Recorded {
		t.Fatalf("предпросмотр события: %+v", preview)
	}

	for _, body := range []string{`{`, `{"name":"   "}`, `{}`,
		fmt.Sprintf(`{"name":%q}`, strings.Repeat("я", maxGuestNameLen+1))} {
		expectStatus(t, securityRequest(h, "POST", "/join/"+token, body, nil), http.StatusBadRequest)
	}

	w = securityRequest(h, "POST", "/join/"+token, `{"name":"  Мария  "}`, nil)
	expectStatus(t, w, http.StatusCreated)
	var joined struct {
		userResponse
		LessonID int64 `json:"lesson_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &joined); err != nil {
		t.Fatal(err)
	}
	if joined.Role != store.RoleGuest || joined.Name != "Мария" || joined.LessonID != event.ID {
		t.Fatalf("гость заведён неправильно: %+v", joined)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatal("сессия гостя без защиты cookie")
	}
	guest := cookies[0]
	expectStatus(t, securityRequest(h, "GET", "/auth/me", "", guest), http.StatusOK)
	expectStatus(t, securityRequest(h, "GET", fmt.Sprintf("/lessons/%d", event.ID), "", guest), http.StatusOK)
	// Гость ссылками не распоряжается
	expectStatus(t, securityRequest(h, "POST", fmt.Sprintf("/lessons/%d/guest-link", event.ID), "", guest), http.StatusForbidden)

	// Ссылка многоразовая: второй человек заходит по той же
	expectStatus(t, securityRequest(h, "POST", "/join/"+token, `{"name":"Пётр"}`, nil), http.StatusCreated)
	var participants int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM lesson_participants WHERE lesson_id=$1`, event.ID).Scan(&participants); err != nil || participants != 2 {
		t.Fatalf("участников события %d, ждали 2 (%v)", participants, err)
	}

	// Прошедшее событие: ссылка жива, но заходить уже некуда
	past, err := a.store.CreateLesson(ctx, nil, "Вчерашняя встреча", organizerID,
		time.Now().Add(-3*time.Hour), time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetGuestToken(ctx, past.ID, hashToken("past-event-token")); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, securityRequest(h, "GET", "/join/past-event-token", "", nil), http.StatusGone)
	expectStatus(t, securityRequest(h, "POST", "/join/past-event-token", `{"name":"Опоздавший"}`, nil), http.StatusGone)
}

// Перевыпуск или отзыв, начатый раньше входа гостя, обязан победить: старый
// токен не заводит гостя, даже если вход прочитал урок до коммита UPDATE.
func TestGuestJoinLosesToConcurrentReissueAndRevoke(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	organizerID, _ := securityUser(t, a, conn, store.RoleTeacher, "race-organizer")
	// Отдельное соединение держит незакоммиченный UPDATE, как SetGuestToken
	// посреди перевыпуска; conn остаётся свободным для наблюдения за блокировками.
	locker, err := pgx.ConnectConfig(ctx, conn.Config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locker.Close(ctx) })
	h := a.Router()

	for _, tc := range []struct {
		name    string
		newHash string
	}{
		{"reissue", hashToken("race-reissued")},
		{"revoke", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := openEvent(t, a, organizerID, "Гонка "+tc.name)
			oldToken := "race-old-" + tc.name
			if err := a.store.SetGuestToken(ctx, event.ID, hashToken(oldToken)); err != nil {
				t.Fatal(err)
			}
			tx, err := locker.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `UPDATE lessons SET guest_token_hash = NULLIF($2, '') WHERE id = $1`,
				event.ID, tc.newHash); err != nil {
				t.Fatal(err)
			}

			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- securityRequest(h, "POST", "/join/"+oldToken, `{"name":"Опоздавший"}`, nil)
			}()
			waitBlockedBy(t, conn, int(locker.PgConn().PID()), done)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				expectStatus(t, w, http.StatusNotFound)
			case <-time.After(10 * time.Second):
				t.Fatal("вход гостя не завершился после коммита")
			}
			var participants int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM lesson_participants WHERE lesson_id=$1`, event.ID).Scan(&participants); err != nil || participants != 0 {
				t.Fatalf("по отозванной ссылке зашло %d гостей (%v)", participants, err)
			}
			if tc.newHash != "" {
				expectStatus(t, securityRequest(h, "GET", "/join/race-reissued", "", nil), http.StatusOK)
			}
		})
	}
}

// waitBlockedBy ждёт, пока чей-то запрос встанет в очередь за блокировкой
// соединения pid. Вход, закончившийся раньше, — уже ошибка: он не мог
// видеть незакоммиченный перевыпуск и обязан был его дождаться.
func waitBlockedBy(t *testing.T, conn *pgx.Conn, pid int, done <-chan *httptest.ResponseRecorder) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case w := <-done:
			t.Fatalf("вход не дождался перевыпуска: %d %s", w.Code, w.Body.String())
		default:
		}
		var blocked bool
		if err := conn.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY (pg_blocking_pids(pid)))`,
			pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("вход гостя так и не упёрся в блокировку перевыпуска")
}

func TestOpenEventRulesInAPIAndSchema(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	teacherID, teacher := securityUser(t, a, conn, store.RoleTeacher, "rules-teacher")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('A2 утро','A2') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	times := `"starts_at":"2030-01-01T10:00:00Z","ends_at":"2030-01-01T11:00:00Z"`

	expectStatus(t, securityRequest(h, "POST", "/lessons", `{"group_id":null,"title":"   ",`+times+`}`, teacher), http.StatusBadRequest)
	expectStatus(t, securityRequest(h, "POST", "/lessons", fmt.Sprintf(`{"group_id":%d,"title":"Своё название",%s}`, groupID, times), teacher), http.StatusBadRequest)
	w := securityRequest(h, "POST", "/lessons", fmt.Sprintf(`{"group_id":%d,"title":"  ",%s}`, groupID, times), teacher)
	expectStatus(t, w, http.StatusCreated)
	var lesson lessonResponse
	if err := json.Unmarshal(w.Body.Bytes(), &lesson); err != nil || lesson.Title != "" {
		t.Fatalf("урок с группой сохранил title %q (%v)", lesson.Title, err)
	}
	w = securityRequest(h, "POST", "/lessons", `{"group_id":null,"title":" Клуб ",`+times+`}`, teacher)
	expectStatus(t, w, http.StatusCreated)
	if err := json.Unmarshal(w.Body.Bytes(), &lesson); err != nil || lesson.Title != "Клуб" || lesson.GroupID != nil {
		t.Fatalf("событие создано неправильно: %+v (%v)", lesson, err)
	}

	// Те же правила держит схема (0006): мимо api их не обойти
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO lessons(group_id,title,teacher_id,starts_at,ends_at) VALUES (NULL,'  ',$1,now(),now()+interval '1 hour')`, []any{teacherID}},
		{`INSERT INTO lessons(group_id,title,teacher_id,starts_at,ends_at) VALUES (NULL,NULL,$1,now(),now()+interval '1 hour')`, []any{teacherID}},
		{`INSERT INTO lessons(group_id,title,teacher_id,starts_at,ends_at) VALUES ($2,'Чужое',$1,now(),now()+interval '1 hour')`, []any{teacherID, groupID}},
		{`INSERT INTO lessons(group_id,teacher_id,starts_at,ends_at,guest_token_hash) VALUES ($2,$1,now(),now()+interval '1 hour','digest')`, []any{teacherID, groupID}},
	} {
		_, err := conn.Exec(ctx, stmt.sql, stmt.args...)
		if store.PgErrorCode(err) != pgCheckViolation {
			t.Fatalf("схема пропустила %q: %v", stmt.sql, err)
		}
	}
}
