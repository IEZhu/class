package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
)

// Архивация учёток (долг D-5, ADR-024) на схеме security_test.

func listedUserIDs(t *testing.T, h http.Handler, query string, cookie *http.Cookie) map[int64]*time.Time {
	t.Helper()
	w := securityRequest(h, "GET", "/users"+query, "", cookie)
	expectStatus(t, w, http.StatusOK)
	var users []userWithGroupsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &users); err != nil {
		t.Fatal(err)
	}
	out := map[int64]*time.Time{}
	for _, u := range users {
		out[u.ID] = u.DeactivatedAt
	}
	return out
}

func login(h http.Handler, email string) int {
	return securityRequest(h, "POST", "/auth/login",
		fmt.Sprintf(`{"email":%q,"password":"fixture-password"}`, email), nil).Code
}

func TestUserArchiveRevokesSessionsAndLogin(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	h := a.Router()
	adminID, admin := securityUser(t, a, conn, store.RoleAdmin, "archive-admin")
	teacherID, teacher := securityUser(t, a, conn, store.RoleTeacher, "archive-teacher")
	colleagueID, _ := securityUser(t, a, conn, store.RoleTeacher, "archive-colleague")
	studentID, student := securityUser(t, a, conn, store.RoleStudent, "archive-student")
	strangerID, stranger := securityUser(t, a, conn, store.RoleStudent, "archive-stranger")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('archive','A2') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	// Коллега-преподаватель в группе: staff-учётка в group_members не даёт
	// преподавателю права на неё (ADR-007)
	for _, id := range []int64{teacherID, colleagueID, studentID} {
		if _, err := conn.Exec(ctx, `INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, groupID, id); err != nil {
			t.Fatal(err)
		}
	}
	deactivate := func(id int64) string { return fmt.Sprintf("/users/%d/deactivate", id) }
	reactivate := func(id int64) string { return fmt.Sprintf("/users/%d/reactivate", id) }

	// Границы прав: студент — никого, преподаватель — не staff и не чужих,
	// себя — никто
	expectStatus(t, securityRequest(h, "POST", deactivate(strangerID), "", student), http.StatusForbidden)
	expectStatus(t, securityRequest(h, "POST", deactivate(studentID), "", nil), http.StatusUnauthorized)
	for _, id := range []int64{colleagueID, adminID, strangerID} {
		expectStatus(t, securityRequest(h, "POST", deactivate(id), "", teacher), http.StatusForbidden)
	}
	expectStatus(t, securityRequest(h, "POST", deactivate(adminID), "", admin), http.StatusForbidden)
	expectStatus(t, securityRequest(h, "POST", deactivate(999999), "", admin), http.StatusNotFound)
	expectStatus(t, securityRequest(h, "GET", "/users?archived=maybe", "", admin), http.StatusBadRequest)

	// У студента две живые сессии: фикстурная и свежий логин
	if code := login(h, "archive-student@example.com"); code != http.StatusOK {
		t.Fatalf("логин до архивации: %d", code)
	}
	countSessions := func(id int64) int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countSessions(studentID); n != 2 {
		t.Fatalf("у студента %d сессий, хотели 2", n)
	}

	// Преподаватель архивирует своего студента: отметка и гашение сессий разом
	expectStatus(t, securityRequest(h, "POST", deactivate(studentID), "", teacher), http.StatusNoContent)
	if n := countSessions(studentID); n != 0 {
		t.Fatalf("после архивации осталось %d сессий", n)
	}
	expectStatus(t, securityRequest(h, "GET", "/auth/me", "", student), http.StatusUnauthorized)
	var first time.Time
	if err := conn.QueryRow(ctx, `SELECT deactivated_at FROM users WHERE id = $1`, studentID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	// Повтор идемпотентен и момент архивации не сдвигает
	expectStatus(t, securityRequest(h, "POST", deactivate(studentID), "", admin), http.StatusNoContent)
	var again time.Time
	if err := conn.QueryRow(ctx, `SELECT deactivated_at FROM users WHERE id = $1`, studentID).Scan(&again); err != nil || !again.Equal(first) {
		t.Fatalf("повторная архивация сдвинула отметку: %v → %v (%v)", first, again, err)
	}

	// Вход закрыт: верный пароль — 403 с понятным кодом, неверный — как всегда 401
	w := securityRequest(h, "POST", "/auth/login", `{"email":"archive-student@example.com","password":"fixture-password"}`, nil)
	expectStatus(t, w, http.StatusForbidden)
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("архивированному выдан cookie")
	}
	expectStatus(t, securityRequest(h, "POST", "/auth/login", `{"email":"archive-student@example.com","password":"wrong-password"}`, nil), http.StatusUnauthorized)
	// Сессия, дописанная после архивации (логин в гонке с ней), не пускает
	if err := a.store.CreateSession(ctx, studentID, hashToken("late-session"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, securityRequest(h, "GET", "/auth/me", "", &http.Cookie{Name: sessionCookie, Value: "late-session"}), http.StatusUnauthorized)

	// Списки: по умолчанию архива нет, с фильтром — только архив
	if _, ok := listedUserIDs(t, h, "", admin)[studentID]; ok {
		t.Fatal("архивный студент в списке админа по умолчанию")
	}
	if _, ok := listedUserIDs(t, h, "", teacher)[studentID]; ok {
		t.Fatal("архивный студент в списке преподавателя по умолчанию")
	}
	for _, cookie := range []*http.Cookie{admin, teacher} {
		archived := listedUserIDs(t, h, "?archived=1", cookie)
		if at, ok := archived[studentID]; !ok || at == nil || len(archived) != 1 {
			t.Fatalf("архив по фильтру: %v", archived)
		}
	}
	// В новый урок группы архивный не попадает, в группе остаётся с отметкой
	lesson, err := a.store.CreateLesson(ctx, &groupID, "", teacherID, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := a.store.IsLessonParticipant(ctx, lesson.ID, studentID); err != nil || ok {
		t.Fatalf("архивный студент в снапшоте нового урока: %v %v", ok, err)
	}
	w = securityRequest(h, "GET", "/groups", "", admin)
	expectStatus(t, w, http.StatusOK)
	var groups []groupResponse
	if err := json.Unmarshal(w.Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	for _, m := range groups[0].Members {
		if m.UserID == studentID && !m.Archived {
			t.Fatal("в составе группы нет отметки архива")
		}
	}

	// Возврат: преподаватель чужого не вернёт, админ вернёт, вход по прежнему паролю
	expectStatus(t, securityRequest(h, "POST", reactivate(strangerID), "", teacher), http.StatusForbidden)
	expectStatus(t, securityRequest(h, "POST", reactivate(studentID), "", admin), http.StatusNoContent)
	if code := login(h, "archive-student@example.com"); code != http.StatusOK {
		t.Fatalf("логин после возврата: %d", code)
	}
	if at, ok := listedUserIDs(t, h, "", admin)[studentID]; !ok || at != nil {
		t.Fatalf("вернувшийся не в основном списке: %v %v", ok, at)
	}
	// Посторонний студент работал всё это время
	expectStatus(t, securityRequest(h, "GET", "/auth/me", "", stranger), http.StatusOK)
}

// Миграция 0013 обратима: down поверх данных (архивная учётка,
// подтверждённый материал с файлом) проходит, а повторный up снова применяется.
func TestMigration0013Reversible(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	teacherID, _ := securityUser(t, a, conn, store.RoleTeacher, "migration-teacher")
	studentID, _ := securityUser(t, a, conn, store.RoleStudent, "migration-student")
	lesson, err := a.store.CreateLesson(ctx, nil, "Событие", teacherID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.store.CreateMaterial(ctx, lesson.ID, "material", "Слайды", "текст", &store.MaterialFile{
		S3Key: fmt.Sprintf("materials/%d/0123456789abcdef0123456789abcdef.pdf", lesson.ID),
		Name:  "slides.pdf", ContentType: "application/pdf", Size: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.ConfirmMaterialFile(ctx, m.ID, `"etag"`); err != nil {
		t.Fatal(err)
	}
	if err := a.store.DeactivateUser(ctx, studentID); err != nil {
		t.Fatal(err)
	}
	columns := func() int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND (table_name, column_name) IN (('users','deactivated_at'), ('materials','file_name'),
			      ('materials','file_content_type'), ('materials','file_size'), ('materials','file_uploaded_at'),
			      ('materials','file_etag'))`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	apply := func(file string) {
		t.Helper()
		sql, err := os.ReadFile("../../migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	if n := columns(); n != 6 {
		t.Fatalf("после up колонок 0013: %d", n)
	}
	apply("0013_material_files_archive.down.sql")
	if n := columns(); n != 0 {
		t.Fatalf("после down осталось колонок 0013: %d", n)
	}
	var title string
	var key *string
	if err := conn.QueryRow(ctx, `SELECT title, s3_key FROM materials WHERE id = $1`, m.ID).Scan(&title, &key); err != nil ||
		title != "Слайды" || key != nil {
		t.Fatalf("материал после down: %q %v (%v)", title, key, err)
	}
	apply("0013_material_files_archive.up.sql")
	if n := columns(); n != 6 {
		t.Fatalf("после повторного up колонок 0013: %d", n)
	}
}
