package jobs

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const periodicKind = "periodic_kind"

func countKind(t *testing.T, pool *pgxpool.Pool, kind string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM jobs WHERE kind = $1`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// passPeriod — «прошёл период»: занятый слот сдвигается на период назад,
// и текущий период снова свободен. Так тест не ждёт настоящего часа.
func passPeriod(t *testing.T, pool *pgxpool.Pool, kind string, every time.Duration) {
	t.Helper()
	exec(t, pool, `UPDATE job_schedules SET slot = slot - make_interval(secs => $2) WHERE kind = $1`,
		kind, every.Seconds())
}

// Несколько worker'ов тикают одновременно и много раз: за период в очереди
// ровно одна джоба. Новые экземпляры Scheduler (перезапуск worker'а) в том
// же периоде ничего не добавляют; следующий период — ровно ещё одна.
func TestSchedulerEnqueuesOncePerPeriodAcrossRunners(t *testing.T) {
	pool := testDB(t)
	const every = time.Hour
	tickAll := func(runners int) {
		t.Helper()
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range runners {
			s := NewScheduler(pool)
			s.Every(periodicKind, every)
			wg.Go(func() {
				<-start
				for range 5 {
					if _, err := s.Tick(context.Background()); err != nil {
						t.Error(err)
						return
					}
				}
			})
		}
		close(start)
		wg.Wait()
	}

	tickAll(4)
	if n := countKind(t, pool, periodicKind); n != 1 {
		t.Fatalf("first period enqueued %d jobs, want 1", n)
	}

	// «Перезапуск»: свежие планировщики, тот же период
	tickAll(2)
	if n := countKind(t, pool, periodicKind); n != 1 {
		t.Fatalf("restart within the period enqueued %d jobs, want 1", n)
	}

	// Джобу выполнили, наступил следующий период
	exec(t, pool, `DELETE FROM jobs WHERE kind = $1`, periodicKind)
	passPeriod(t, pool, periodicKind, every)
	tickAll(4)
	if n := countKind(t, pool, periodicKind); n != 1 {
		t.Fatalf("next period enqueued %d jobs, want 1", n)
	}
}

// Пока прошлая джоба ждёт или выполняется, новый период её не дублирует;
// мёртвая джоба новую не держит.
func TestSchedulerSkipsWhilePreviousJobIsAlive(t *testing.T) {
	pool := testDB(t)
	const every = 5 * time.Minute
	s := NewScheduler(pool)
	s.Every(periodicKind, every)
	tick := func() []string {
		t.Helper()
		enqueued, err := s.Tick(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return enqueued
	}
	if got := tick(); len(got) != 1 || got[0] != periodicKind {
		t.Fatalf("first tick enqueued %v", got)
	}

	passPeriod(t, pool, periodicKind, every)
	if got := tick(); len(got) != 0 {
		t.Fatalf("enqueued %v while the previous job is pending", got)
	}
	if n := countKind(t, pool, periodicKind); n != 1 {
		t.Fatalf("%d jobs, want the pending one only", n)
	}

	exec(t, pool, `UPDATE jobs SET failed_at = now() WHERE kind = $1`, periodicKind)
	passPeriod(t, pool, periodicKind, every)
	if got := tick(); len(got) != 1 {
		t.Fatalf("dead job blocked the schedule: %v", got)
	}
	if n := countKind(t, pool, periodicKind); n != 2 {
		t.Fatalf("%d jobs, want the dead one and a fresh one", n)
	}
}

// Поставленная по расписанию джоба — обычная джоба очереди: её берёт
// Runner, а расписания разных kind не мешают друг другу.
func TestScheduledJobsRunThroughTheQueue(t *testing.T) {
	pool := testDB(t)
	s := NewScheduler(pool)
	s.Every(periodicKind, time.Hour)
	s.Every(testKind, time.Minute)
	if got, err := s.Tick(context.Background()); err != nil || len(got) != 2 {
		t.Fatalf("Tick = %v, %v", got, err)
	}
	ran := map[string]int{}
	r := NewRunner(pool, testConfig("periodic"))
	for _, kind := range []string{periodicKind, testKind} {
		r.Handle(kind, func(_ context.Context, job Job) error {
			ran[job.Kind]++
			if string(job.Payload) != "{}" {
				return fmt.Errorf("payload %s", job.Payload)
			}
			return nil
		})
	}
	for runOnce(t, r) {
	}
	if ran[periodicKind] != 1 || ran[testKind] != 1 || count(t, pool) != 0 {
		t.Fatalf("ran %v, %d jobs left", ran, count(t, pool))
	}
}

func TestSchedulerRejectsBadPeriod(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("sub-second period accepted")
		}
	}()
	NewScheduler(nil).Every(periodicKind, time.Millisecond)
}

// Run тикает сразу при старте и выходит по отмене контекста.
func TestSchedulerRunTicksImmediatelyAndStops(t *testing.T) {
	pool := testDB(t)
	s := NewScheduler(pool)
	s.Every(periodicKind, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for countKind(t, pool, periodicKind) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Run did not tick at start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
