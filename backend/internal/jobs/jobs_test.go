package jobs

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IEZhu/class/backend/internal/testdb"
)

const testKind = "test_kind"

// testDB — пул на собственную схему одноразовой базы с настоящими миграциями
// (internal/testdb, общий с httpapi).
func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testdb.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// testConfig — быстрые таймауты; backoff настоящий, а «ожидание» тесты
// проматывают, сдвигая run_at.
func testConfig(id string) Config {
	return Config{WorkerID: id, PollInterval: 10 * time.Millisecond,
		MaxAttempts: 3, BaseBackoff: time.Minute, MaxBackoff: time.Hour,
		HandlerTimeout: time.Second, ShutdownGrace: time.Second}
}

func enqueue(t *testing.T, db DB) int64 {
	t.Helper()
	id, err := Enqueue(context.Background(), db, testKind, map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func runOnce(t *testing.T, r *Runner) bool {
	t.Helper()
	worked, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return worked
}

type jobRow struct {
	attempts  int
	lastError string
	locked    bool
	dead      bool
	delay     time.Duration // run_at - now()
}

func row(t *testing.T, pool *pgxpool.Pool, id int64) jobRow {
	t.Helper()
	var j jobRow
	var seconds float64
	err := pool.QueryRow(context.Background(),
		`SELECT attempts, coalesce(last_error, ''), locked_at IS NOT NULL, failed_at IS NOT NULL,
		        extract(epoch FROM run_at - now())::float8
		 FROM jobs WHERE id = $1`, id).Scan(&j.attempts, &j.lastError, &j.locked, &j.dead, &seconds)
	if err != nil {
		t.Fatal(err)
	}
	j.delay = time.Duration(seconds * float64(time.Second))
	return j
}

// Модель данных (03-data-model.md) плюс failed_at и created_at (ADR-013);
// откат миграции убирает таблицу, повторный накат возвращает.
func TestMigrationMatchesDataModelAndReverts(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	columns := func() []string {
		rows, err := pool.Query(ctx, `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'jobs' ORDER BY column_name`)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	want := "attempts,created_at,failed_at,id,kind,last_error,locked_at,locked_by,payload,run_at"
	if got := strings.Join(columns(), ","); got != want {
		t.Fatalf("columns %s, want %s", got, want)
	}
	testdb.Apply(t, pool, "0007_jobs.down.sql")
	if got := columns(); len(got) != 0 {
		t.Fatalf("down left columns %v", got)
	}
	testdb.Apply(t, pool, "0007_jobs.up.sql")
	if got := strings.Join(columns(), ","); got != want {
		t.Fatalf("columns after re-up %s", got)
	}
}

// Ради этого очередь в Postgres: джоба живёт и умирает вместе с транзакцией.
func TestEnqueueFollowsCallerTransaction(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	enqueue(t, tx)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool); n != 0 {
		t.Fatalf("rolled back enqueue left %d jobs", n)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	enqueue(t, tx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool); n != 1 {
		t.Fatalf("committed enqueue left %d jobs", n)
	}
}

func TestConcurrentClaimersNeverShareJob(t *testing.T) {
	pool := testDB(t)
	const total, workers = 40, 6
	for range total {
		enqueue(t, pool)
	}
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for w := range workers {
		r := NewRunner(pool, testConfig(fmt.Sprintf("worker-%d", w)))
		r.Handle(testKind, func(_ context.Context, job Job) error {
			mu.Lock()
			seen[job.ID]++
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			return nil
		})
		wg.Go(func() {
			for {
				worked, err := r.RunOnce(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if !worked {
					return
				}
			}
		})
	}
	wg.Wait()
	if len(seen) != total {
		t.Fatalf("processed %d distinct jobs, want %d", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("job %d processed %d times", id, n)
		}
	}
	if n := count(t, pool); n != 0 {
		t.Fatalf("%d jobs left after success", n)
	}
}

// Строку, которую держит другой захват, runner пропускает, а не ждёт.
func TestClaimSkipsLockedRowWithoutWaiting(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	first, second := enqueue(t, pool), enqueue(t, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var held int64
	if err := tx.QueryRow(ctx, `SELECT id FROM jobs WHERE id = $1 FOR UPDATE`, first).Scan(&held); err != nil {
		t.Fatal(err)
	}
	var got []int64
	r := NewRunner(pool, testConfig("skipper"))
	r.Handle(testKind, func(_ context.Context, job Job) error { got = append(got, job.ID); return nil })
	quick, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if worked, err := r.RunOnce(quick); err != nil || !worked {
		t.Fatalf("RunOnce = %v, %v", worked, err)
	}
	if worked, err := r.RunOnce(quick); err != nil || worked {
		t.Fatalf("locked row was not skipped: %v, %v", worked, err)
	}
	if len(got) != 1 || got[0] != second {
		t.Fatalf("claimed %v, want only %d", got, second)
	}
}

func TestFailureRetriesWithGrowingBackoffThenDies(t *testing.T) {
	pool := testDB(t)
	id := enqueue(t, pool)
	calls := 0
	r := NewRunner(pool, testConfig("retrier"))
	r.Handle(testKind, func(context.Context, Job) error {
		calls++
		return fmt.Errorf("boom %d", calls)
	})

	var delays []time.Duration
	for attempt := 1; attempt <= 3; attempt++ {
		if !runOnce(t, r) {
			t.Fatalf("attempt %d was not claimed", attempt)
		}
		j := row(t, pool, id)
		if j.attempts != attempt || j.lastError != fmt.Sprintf("boom %d", attempt) || j.locked {
			t.Fatalf("after attempt %d: %+v", attempt, j)
		}
		if attempt < 3 {
			if j.dead {
				t.Fatalf("job died after attempt %d of 3", attempt)
			}
			delays = append(delays, j.delay)
			if runOnce(t, r) {
				t.Fatal("job claimed before its backoff elapsed")
			}
			exec(t, pool, `UPDATE jobs SET run_at = now() WHERE id = $1`, id)
		} else if !j.dead {
			t.Fatalf("job still alive after max attempts: %+v", j)
		}
	}
	// 1 мин, затем 2 мин: допуск — на время самого теста
	for i, want := range []time.Duration{time.Minute, 2 * time.Minute} {
		if d := delays[i]; d < want-5*time.Second || d > want {
			t.Errorf("retry %d delay %s, want ~%s", i+1, d, want)
		}
	}
	exec(t, pool, `UPDATE jobs SET run_at = now() WHERE id = $1`, id)
	if runOnce(t, r) {
		t.Fatal("dead job was claimed again")
	}
	if calls != 3 {
		t.Fatalf("handler called %d times, want 3", calls)
	}
}

func TestBackoffDoublesUpToCap(t *testing.T) {
	c := Config{BaseBackoff: time.Minute, MaxBackoff: time.Hour}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60}
	for i, minutes := range want {
		if got := c.backoff(i + 1); got != minutes*time.Minute {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, minutes*time.Minute)
		}
	}
	if got := c.backoff(1000); got != time.Hour {
		t.Errorf("backoff(1000) = %s, want cap", got)
	}
}

// Упавший worker не отписался: его захват истёк, джобу берёт следующий.
// Свежий чужой захват при этом не трогается.
func TestStaleLockIsReclaimed(t *testing.T) {
	pool := testDB(t)
	stale, fresh := enqueue(t, pool), enqueue(t, pool)
	exec(t, pool, `UPDATE jobs SET locked_at = now() - interval '1 hour', locked_by = 'crashed', attempts = 1
		WHERE id = $1`, stale)
	exec(t, pool, `UPDATE jobs SET locked_at = now(), locked_by = 'busy', attempts = 1 WHERE id = $1`, fresh)

	var got []Job
	var inFlightError string
	r := NewRunner(pool, testConfig("rescuer"))
	r.Handle(testKind, func(ctx context.Context, job Job) error {
		got = append(got, job)
		return pool.QueryRow(ctx, `SELECT last_error FROM jobs WHERE id = $1`, job.ID).Scan(&inFlightError)
	})
	if !runOnce(t, r) || runOnce(t, r) {
		t.Fatalf("claimed %v, want only the stale job", got)
	}
	if len(got) != 1 || got[0].ID != stale || got[0].Attempt != 2 {
		t.Fatalf("claimed %+v, want job %d attempt 2", got, stale)
	}
	if !strings.Contains(inFlightError, "crashed") {
		t.Errorf("reclaim did not record the lost owner: %q", inFlightError)
	}
	var owner string
	if err := pool.QueryRow(context.Background(), `SELECT locked_by FROM jobs WHERE id = $1`, fresh).Scan(&owner); err != nil || owner != "busy" {
		t.Fatalf("fresh lock changed: %q, %v", owner, err)
	}
	if n := count(t, pool); n != 1 {
		t.Fatalf("%d jobs left, want only the fresh one", n)
	}
}

// Джоба, которая раз за разом роняет worker, умирает, не исполняясь снова.
func TestStaleLockAfterMaxAttemptsDies(t *testing.T) {
	pool := testDB(t)
	id := enqueue(t, pool)
	exec(t, pool, `UPDATE jobs SET locked_at = now() - interval '1 hour', locked_by = 'crashed', attempts = 3
		WHERE id = $1`, id)
	r := NewRunner(pool, testConfig("rescuer"))
	r.Handle(testKind, func(context.Context, Job) error {
		t.Error("handler ran a job past its max attempts")
		return nil
	})
	if !runOnce(t, r) {
		t.Fatal("stale job was not picked up")
	}
	if j := row(t, pool, id); !j.dead || j.locked || !strings.Contains(j.lastError, "исчерпаны") {
		t.Fatalf("job not dead: %+v", j)
	}
}

// Запоздалый итог прошлого владельца не трогает джобу, которую уже подобрал
// другой worker.
func TestLostLockDoesNotSettle(t *testing.T) {
	pool := testDB(t)
	id := enqueue(t, pool)
	r := NewRunner(pool, testConfig("slow"))
	r.Handle(testKind, func(ctx context.Context, job Job) error {
		_, err := pool.Exec(ctx, `UPDATE jobs SET locked_by = 'other', attempts = attempts + 1 WHERE id = $1`, job.ID)
		return err
	})
	if !runOnce(t, r) {
		t.Fatal("job was not claimed")
	}
	if j := row(t, pool, id); j.attempts != 2 || !j.locked {
		t.Fatalf("stale owner settled a job it no longer holds: %+v", j)
	}
}

func TestPanicIsFailedAttempt(t *testing.T) {
	pool := testDB(t)
	id := enqueue(t, pool)
	r := NewRunner(pool, testConfig("panicky"))
	r.Handle(testKind, func(context.Context, Job) error { panic("kaboom") })
	if !runOnce(t, r) {
		t.Fatal("job was not claimed")
	}
	if j := row(t, pool, id); j.attempts != 1 || j.locked || j.dead || !strings.Contains(j.lastError, "kaboom") {
		t.Fatalf("after panic: %+v", j)
	}
}

func TestUnknownKindWaitsForItsWorker(t *testing.T) {
	pool := testDB(t)
	if _, err := Enqueue(context.Background(), pool, "future_kind", nil); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(pool, testConfig("old"))
	r.Handle(testKind, func(context.Context, Job) error { return nil })
	if runOnce(t, r) {
		t.Fatal("runner claimed a kind it has no handler for")
	}
}

// Остановка: текущая джоба либо успевает за ShutdownGrace, либо
// прерывается и возвращается в очередь без списания попытки.
func TestShutdownFinishesOrReleasesCurrentJob(t *testing.T) {
	run := func(t *testing.T, grace time.Duration, h Handler) *pgxpool.Pool {
		pool := testDB(t)
		enqueue(t, pool)
		cfg := testConfig("stopping")
		cfg.ShutdownGrace = grace
		r := NewRunner(pool, cfg)
		started := make(chan struct{})
		r.Handle(testKind, func(ctx context.Context, job Job) error {
			close(started)
			return h(ctx, job)
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { r.Run(ctx); close(done) }()
		<-started
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after shutdown")
		}
		return pool
	}

	t.Run("finishes within grace", func(t *testing.T) {
		pool := run(t, 2*time.Second, func(ctx context.Context, _ Job) error {
			time.Sleep(100 * time.Millisecond)
			return ctx.Err()
		})
		if n := count(t, pool); n != 0 {
			t.Fatalf("finished job left %d rows", n)
		}
	})

	t.Run("released after grace", func(t *testing.T) {
		pool := run(t, 100*time.Millisecond, func(ctx context.Context, _ Job) error {
			<-ctx.Done()
			return ctx.Err()
		})
		var attempts int
		var locked, dead bool
		err := pool.QueryRow(context.Background(),
			`SELECT attempts, locked_at IS NOT NULL, failed_at IS NOT NULL FROM jobs`).Scan(&attempts, &locked, &dead)
		if err != nil || attempts != 0 || locked || dead {
			t.Fatalf("released job: attempts=%d locked=%v dead=%v err=%v", attempts, locked, dead, err)
		}
		// Следующий worker берёт её как новую
		r := NewRunner(pool, testConfig("next"))
		var attempt int
		r.Handle(testKind, func(_ context.Context, job Job) error { attempt = job.Attempt; return nil })
		if !runOnce(t, r) || attempt != 1 {
			t.Fatalf("released job not reclaimed as attempt 1 (got %d)", attempt)
		}
	})
}
