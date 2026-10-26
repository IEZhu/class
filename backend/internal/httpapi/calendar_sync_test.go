package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/worker"
)

// calendarWorker — worker с одной джобой calendar_sync против фейка Google;
// drain выполняет всё, что готово к запуску.
func calendarWorker(t *testing.T, a *API) func() {
	t.Helper()
	runner := jobs.NewRunner(a.store.Pool(), jobs.Config{WorkerID: "test-worker"})
	worker.RegisterCalendar(runner, a.store, a.google, "https://class.example")
	return func() {
		t.Helper()
		for {
			worked, err := runner.RunOnce(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !worked {
				return
			}
		}
	}
}

func lessonEventID(t *testing.T, conn *pgx.Conn, lessonID int64) string {
	t.Helper()
	var id *string
	if err := conn.QueryRow(context.Background(), `SELECT gcal_event_id FROM lessons WHERE id = $1`, lessonID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id == nil {
		t.Fatalf("lesson %d has no gcal_event_id", lessonID)
	}
	return *id
}

func expectNoPendingJobs(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var n int
	var lastError *string
	err := conn.QueryRow(context.Background(),
		`SELECT count(*), max(last_error) FROM jobs`).Scan(&n, &lastError)
	if err != nil || n != 0 {
		t.Fatalf("jobs left: %d (%v), last_error %v", n, err, lastError)
	}
}

func lessonTimes(start time.Time) string {
	return fmt.Sprintf(`"starts_at":%q,"ends_at":%q`, start.Format(time.RFC3339), start.Add(time.Hour).Format(time.RFC3339))
}

// Полный цикл calendar_sync на БД и фейке Google: создание — insert с
// участниками и sendUpdates=all, перенос — update, отмена — delete; повторы
// джоб не плодят событий; без подключения урок пропускается с записью
// в журнале; отозванный доступ снимает подключение.
func TestCalendarSyncLifecycle(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	f := withFakeGoogle(t, a)
	h := a.Router()
	logs := captureLog(t)
	drain := calendarWorker(t, a)

	teacherID, teacher := securityUser(t, a, conn, store.RoleTeacher, "teacher")
	student1, _ := securityUser(t, a, conn, store.RoleStudent, "student1")
	student2, _ := securityUser(t, a, conn, store.RoleStudent, "student2")
	guestID, _ := securityUser(t, a, conn, store.RoleGuest, "guest")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('Morning B1','B1') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	// Преподаватель в составе своей группы — в приглашённые не попадает
	for _, id := range []int64{student1, student2, teacherID} {
		if _, err := conn.Exec(ctx, `INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, groupID, id); err != nil {
			t.Fatal(err)
		}
	}
	connectTeacherGoogle(t, f, h, teacher)
	drain()

	start := time.Now().Add(24 * time.Hour).Truncate(time.Second).UTC()
	createBody := fmt.Sprintf(`{"group_id":%d,%s}`, groupID, lessonTimes(start))

	// Джоба ставится в транзакции урока: без очереди нет и урока
	if _, err := conn.Exec(ctx, `ALTER TABLE jobs RENAME TO jobs_offline`); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, securityRequest(h, "POST", "/lessons", createBody, teacher), http.StatusInternalServerError)
	if _, err := conn.Exec(ctx, `ALTER TABLE jobs_offline RENAME TO jobs`); err != nil {
		t.Fatal(err)
	}
	var lessons int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM lessons`).Scan(&lessons); err != nil || lessons != 0 {
		t.Fatalf("lesson committed without its calendar job: %d, %v", lessons, err)
	}

	w := securityRequest(h, "POST", "/lessons", createBody, teacher)
	expectStatus(t, w, http.StatusCreated)
	var created lessonResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	// Гость события в приглашённые не попадает: email у него синтетический
	if _, err := conn.Exec(ctx, `INSERT INTO lesson_participants(lesson_id,user_id) VALUES($1,$2)`, created.ID, guestID); err != nil {
		t.Fatal(err)
	}
	eventID := lessonEventID(t, conn, created.ID)
	if got := calendarJobs(t, conn); len(got) != 1 || got[0] != (store.CalendarSync{LessonID: created.ID}) {
		t.Fatalf("create queued %v", got)
	}
	drain()
	expectNoPendingJobs(t, conn)
	if got := f.takeCalls(); !slices.Equal(got, []string{"POST " + eventID}) {
		t.Fatalf("create calls %v", got)
	}
	expectEvent := func(start time.Time) {
		t.Helper()
		f.with(func() {
			ev, ok := f.events[eventID]
			want := []string{"student1@example.com", "student2@example.com"}
			if len(f.events) != 1 || !ok || ev.Summary != "Morning B1" || ev.Status != "confirmed" ||
				ev.Description != fmt.Sprintf("https://class.example/lesson/%d", created.ID) ||
				!ev.Start.DateTime.Equal(start) || !ev.End.DateTime.Equal(start.Add(time.Hour)) ||
				!slices.Equal(ev.attendees(), want) {
				t.Fatalf("events %+v, want %s at %s for %v", f.events, eventID, start, want)
			}
		})
	}
	expectEvent(start)

	// Повторная доставка: тот же id — 409 и update, второго события нет
	if _, err := jobs.Enqueue(ctx, a.store.Pool(), store.JobCalendarSync, store.CalendarSync{LessonID: created.ID}); err != nil {
		t.Fatal(err)
	}
	drain()
	expectNoPendingJobs(t, conn)
	if got := f.takeCalls(); !slices.Equal(got, []string{"POST " + eventID, "PUT " + eventID}) {
		t.Fatalf("redelivery calls %v", got)
	}
	expectEvent(start)

	moved := start.Add(48 * time.Hour)
	path := fmt.Sprintf("/lessons/%d", created.ID)
	expectStatus(t, securityRequest(h, "PATCH", path, "{"+lessonTimes(moved)+"}", teacher), http.StatusOK)
	drain()
	expectNoPendingJobs(t, conn)
	if got := f.takeCalls(); !slices.Equal(got, []string{"POST " + eventID, "PUT " + eventID}) {
		t.Fatalf("reschedule calls %v", got)
	}
	expectEvent(moved)

	// Сбой Google — ошибка попытки и повтор по backoff очереди
	f.with(func() { f.failNext = http.StatusInternalServerError })
	expectStatus(t, securityRequest(h, "PATCH", path, "{"+lessonTimes(start)+"}", teacher), http.StatusOK)
	drain()
	var attempts int
	var lastError string
	if err := conn.QueryRow(ctx, `SELECT attempts, last_error FROM jobs WHERE kind = $1 AND failed_at IS NULL AND run_at > now()`,
		store.JobCalendarSync).Scan(&attempts, &lastError); err != nil || attempts != 1 || !strings.Contains(lastError, "500") {
		t.Fatalf("failed call: attempts %d, last_error %q, %v", attempts, lastError, err)
	}
	if _, err := conn.Exec(ctx, `UPDATE jobs SET run_at = now()`); err != nil {
		t.Fatal(err)
	}
	drain()
	expectNoPendingJobs(t, conn)
	expectEvent(start)
	f.takeCalls()

	expectStatus(t, securityRequest(h, "DELETE", path, "", teacher), http.StatusNoContent)
	cancel := store.CalendarSync{LessonID: created.ID, TeacherID: teacherID, EventID: eventID}
	if got := calendarJobs(t, conn); len(got) != 1 || got[0] != cancel {
		t.Fatalf("cancel queued %v, want %v", got, cancel)
	}
	drain()
	// Повтор отмены: событие уже удалено (410) — тоже успех
	if _, err := jobs.Enqueue(ctx, a.store.Pool(), store.JobCalendarSync, cancel); err != nil {
		t.Fatal(err)
	}
	drain()
	expectNoPendingJobs(t, conn)
	if got := f.takeCalls(); !slices.Equal(got, []string{"DELETE " + eventID, "DELETE " + eventID}) {
		t.Fatalf("cancel calls %v", got)
	}
	f.with(func() {
		if len(f.events) != 0 || !f.deleted[eventID] {
			t.Fatalf("events after cancel %+v", f.events)
		}
	})

	// Отмена раньше первой синхронизации: создавать нечего, удаление — 404
	w = securityRequest(h, "POST", "/lessons", createBody, teacher)
	expectStatus(t, w, http.StatusCreated)
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	early := lessonEventID(t, conn, created.ID)
	expectStatus(t, securityRequest(h, "DELETE", fmt.Sprintf("/lessons/%d", created.ID), "", teacher), http.StatusNoContent)
	drain()
	expectNoPendingJobs(t, conn)
	if got := f.takeCalls(); !slices.Equal(got, []string{"DELETE " + early}) {
		t.Fatalf("early cancel calls %v", got)
	}

	// Преподаватель отключил календарь, пока джоба ждала: пропуск со строкой
	w = securityRequest(h, "POST", "/lessons", createBody, teacher)
	expectStatus(t, w, http.StatusCreated)
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, securityRequest(h, "DELETE", "/auth/google", "", teacher), http.StatusNoContent)
	drain()
	expectNoPendingJobs(t, conn)
	if got := f.takeCalls(); len(got) != 0 {
		t.Fatalf("calls without connection %v", got)
	}
	if want := fmt.Sprintf("урок %d пропущен: у преподавателя %d не подключён Google Calendar", created.ID, teacherID); !strings.Contains(logs.String(), want) {
		t.Fatalf("no skip line %q in log:\n%s", want, logs.String())
	}
	// Без подключения уроки джоб не ставят вовсе
	expectStatus(t, securityRequest(h, "POST", "/lessons", createBody, teacher), http.StatusCreated)
	if got := calendarJobs(t, conn); len(got) != 0 {
		t.Fatalf("jobs without connection %v", got)
	}

	// Доступ отозван в Google: invalid_grant снимает подключение
	connectTeacherGoogle(t, f, h, teacher)
	f.with(func() { f.revoked = true })
	drain()
	expectNoPendingJobs(t, conn)
	if sealed := storedGoogleToken(t, conn, teacherID); sealed != nil {
		t.Fatal("revoked token kept")
	}
	if !strings.Contains(logs.String(), "отозвал доступ к календарю") {
		t.Fatalf("no revoke line in log:\n%s", logs.String())
	}
}
