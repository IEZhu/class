package jobs

import (
	"context"
	"fmt"
	"time"
)

// defaultScheduleTick — как часто Scheduler проверяет расписание. Периоды
// у нас от минут, так что опоздание на полминуты незаметно.
const defaultScheduleTick = 30 * time.Second

// Scheduler ставит периодические джобы (ADR-021): джоба kind попадает
// в очередь не чаще раза за период. Время целиком берётся из Postgres:
// период — отрезок, выровненный по эпохе, а «уже поставлена за этот
// период» помнит строка job_schedules. Поэтому расписание переживает
// перезапуск и не задваивается, сколько бы worker'ов (в том числе на другой
// машине с другими часами) ни тикало одновременно.
type Scheduler struct {
	db    DB
	tick  time.Duration
	every []periodic
}

type periodic struct {
	kind  string
	every time.Duration
}

func NewScheduler(db DB) *Scheduler { return &Scheduler{db: db, tick: defaultScheduleTick} }

// Every регистрирует периодическую джобу до запуска Run. Ставить её имеет
// смысл тому процессу, который её и выполняет: worker без хендлера kind
// копил бы джобы, которые никто не возьмёт.
func (s *Scheduler) Every(kind string, every time.Duration) {
	if kind == "" || every < time.Second {
		panic(fmt.Sprintf("jobs: расписание %q с периодом %s", kind, every))
	}
	s.every = append(s.every, periodic{kind: kind, every: every})
}

// Tick ставит каждую джобу расписания, чей текущий период ещё не занят.
// Возвращает kind поставленных джоб.
func (s *Scheduler) Tick(ctx context.Context) ([]string, error) {
	var enqueued []string
	for _, p := range s.every {
		ok, err := s.claimPeriod(ctx, p)
		if err != nil {
			return enqueued, fmt.Errorf("расписание %s: %w", p.kind, err)
		}
		if ok {
			enqueued = append(enqueued, p.kind)
		}
	}
	return enqueued, nil
}

// claimPeriod — один запрос: занять текущий период и поставить джобу.
// Условный upsert сериализует конкурентов на строке kind: второй ждёт
// коммита первого, видит занятый период и ничего не ставит. Джоба не
// ставится, пока прошлая того же kind ждёт или выполняется, — иначе
// застрявший worker (например, сборщик в паузе после блокировки) копил бы
// их по одной за период. Мёртвая джоба (failed_at) новую не держит.
func (s *Scheduler) claimPeriod(ctx context.Context, p periodic) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`WITH claimed AS (
		     INSERT INTO job_schedules AS s (kind, slot)
		     VALUES ($1, to_timestamp(floor(extract(epoch FROM now())::float8 / $2::float8) * $2::float8))
		     ON CONFLICT (kind) DO UPDATE SET slot = excluded.slot
		     WHERE s.slot < excluded.slot
		     RETURNING kind)
		 INSERT INTO jobs (kind)
		 SELECT kind FROM claimed
		 WHERE NOT EXISTS (SELECT 1 FROM jobs WHERE kind = $1 AND failed_at IS NULL)`,
		p.kind, p.every.Seconds())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Run тикает сразу и затем каждые 30 с, пока не отменён ctx. Без
// расписания возвращается сразу.
func (s *Scheduler) Run(ctx context.Context) {
	if len(s.every) == 0 {
		return
	}
	for {
		enqueued, err := s.Tick(ctx)
		if err != nil && ctx.Err() == nil {
			logf("%v", err)
		}
		for _, kind := range enqueued {
			logf("по расписанию поставлена джоба %s", kind)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.tick):
		}
	}
}
