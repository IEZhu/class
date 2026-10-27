package httpapi

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/worker"
)

// sessionsLeft — оставшиеся сессии: всего и протухших.
func sessionsLeft(t *testing.T, conn *pgx.Conn) (all, expired int) {
	t.Helper()
	if err := conn.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE expires_at <= now()) FROM sessions`).Scan(&all, &expired); err != nil {
		t.Fatal(err)
	}
	return all, expired
}

// sessionCleanupStand — живые и протухшие сессии студента,
// преподавателя и гостя события; протухшие — с разным возрастом.
// Возвращает куки живых сессий и число протухших.
func sessionCleanupStand(t *testing.T) (*API, *pgx.Conn, []*http.Cookie, int) {
	t.Helper()
	a, conn := securityDB(t)
	ctx := context.Background()
	studentID, student := securityUser(t, a, conn, store.RoleStudent, "cleanup-student")
	teacherID, teacher := securityUser(t, a, conn, store.RoleTeacher, "cleanup-teacher")
	var guestID int64
	if err := conn.QueryRow(ctx, `INSERT INTO users (email, role, name)
		VALUES ('guest-cleanup@guest.invalid', $1, 'Гость') RETURNING id`, store.RoleGuest).Scan(&guestID); err != nil {
		t.Fatal(err)
	}
	guestToken, guestHash, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateSession(ctx, guestID, guestHash, time.Now().Add(store.GuestSessionGrace)); err != nil {
		t.Fatal(err)
	}
	expired := 0
	for i, owner := range []int64{studentID, studentID, teacherID, guestID, studentID, guestID, teacherID} {
		_, hash, err := newToken()
		if err != nil {
			t.Fatal(err)
		}
		// i-я протухшая старше (i+1)-й: уборка идёт от самых старых
		if err := a.store.CreateSession(ctx, owner, hash, time.Now().Add(-time.Duration(10-i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
		expired++
	}
	return a, conn, []*http.Cookie{student, teacher, sessionCookieFor(guestToken, 3600)}, expired
}

// Уборка удаляет только протухшие сессии — пользователей и гостей —
// пачками от самых старых и не больше заданного числа пачек за вызов;
// живые сессии продолжают пускать.
func TestSessionCleanupDeletesOnlyExpiredInBatches(t *testing.T) {
	a, conn, live, expired := sessionCleanupStand(t)
	ctx := context.Background()
	all, before := sessionsLeft(t, conn)
	if before != expired || all != expired+len(live) {
		t.Fatalf("стенд: сессий %d, протухших %d", all, before)
	}

	// Две пачки по 2: ушли 4 самые старые, младшие протухшие остались
	n, err := a.store.CleanupExpiredSessions(ctx, 2, 2)
	if err != nil || n != 4 {
		t.Fatalf("две пачки по 2: удалено %d, %v", n, err)
	}
	var oldestLeft time.Time
	if err := conn.QueryRow(ctx, `SELECT min(expires_at) FROM sessions WHERE expires_at <= now()`).Scan(&oldestLeft); err != nil {
		t.Fatal(err)
	}
	if want := time.Now().Add(-6 * time.Hour); oldestLeft.Before(want.Add(-time.Minute)) || oldestLeft.After(want.Add(time.Minute)) {
		t.Fatalf("старейшая оставшаяся протухшая — %s, хотели около %s", oldestLeft, want)
	}

	// Остаток добирает следующий вызов; неполная пачка — конец
	n, err = a.store.CleanupExpiredSessions(ctx, 2, 100)
	if err != nil || n != int64(expired-4) {
		t.Fatalf("остаток: удалено %d, %v", n, err)
	}
	if all, left := sessionsLeft(t, conn); left != 0 || all != len(live) {
		t.Fatalf("после уборки: сессий %d, протухших %d", all, left)
	}
	h := a.Router()
	for _, cookie := range live {
		expectStatus(t, securityRequest(h, "GET", "/auth/me", "", cookie), http.StatusOK)
	}
	// Убирать нечего — ни одной строки и не ошибка
	if n, err := a.store.CleanupExpiredSessions(ctx, 2, 100); err != nil || n != 0 {
		t.Fatalf("повтор: %d, %v", n, err)
	}
}

// Джоба session_cleanup по расписанию: сколько бы worker'ов ни тикало,
// за период — одна джоба; выполненная убирает протухшие сессии; в том же
// периоде новая не ставится, в следующем — ровно одна. Период не ждём:
// занятый слот сдвигается назад (ADR-021).
func TestSessionCleanupScheduledOncePerPeriod(t *testing.T) {
	a, conn, live, _ := sessionCleanupStand(t)
	ctx := context.Background()
	pool := a.store.Pool()
	queued := func() int {
		t.Helper()
		return countRows(t, conn, `SELECT count(*) FROM jobs WHERE kind = $1`, store.JobSessionCleanup)
	}
	runner := jobs.NewRunner(pool, jobs.Config{WorkerID: "cleanup-worker"})
	schedulers := make([]*jobs.Scheduler, 4)
	for i := range schedulers {
		schedulers[i] = jobs.NewScheduler(pool)
		// Хендлер и расписание регистрирует одна функция; хендлер
		// достаточно одного — у runner
		r := runner
		if i > 0 {
			r = jobs.NewRunner(pool, jobs.Config{})
		}
		worker.RegisterSessionCleanup(r, schedulers[i], a.store)
	}
	tickAll := func() {
		t.Helper()
		var wg sync.WaitGroup
		for _, s := range schedulers {
			wg.Go(func() {
				for range 3 {
					if _, err := s.Tick(ctx); err != nil {
						t.Error(err)
					}
				}
			})
		}
		wg.Wait()
	}

	tickAll()
	if n := queued(); n != 1 {
		t.Fatalf("за первый период поставлено %d джоб", n)
	}
	if worked, err := runner.RunOnce(ctx); err != nil || !worked {
		t.Fatalf("RunOnce = %v, %v", worked, err)
	}
	if all, expired := sessionsLeft(t, conn); expired != 0 || all != len(live) || queued() != 0 {
		t.Fatalf("после джобы: сессий %d, протухших %d, джоб %d", all, expired, queued())
	}

	// Тот же период: джоба уже была, новая не ставится
	tickAll()
	if n := queued(); n != 0 {
		t.Fatalf("повтор в том же периоде поставил %d джоб", n)
	}
	// Наступил следующий период — ровно одна
	if _, err := conn.Exec(ctx, `UPDATE job_schedules SET slot = slot - interval '30 days' WHERE kind = $1`,
		store.JobSessionCleanup); err != nil {
		t.Fatal(err)
	}
	tickAll()
	if n := queued(); n != 1 {
		t.Fatalf("следующий период поставил %d джоб", n)
	}
}
