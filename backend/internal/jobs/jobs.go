// Package jobs — очередь фоновых задач в Postgres (ADR-003, ADR-013):
// таблица jobs, захват через SELECT … FOR UPDATE SKIP LOCKED, ретраи
// с экспоненциальным backoff, «мёртвое» состояние после исчерпания попыток
// и подбор джоб, брошенных упавшим worker'ом.
//
// Доставка at-least-once: если worker упадёт между коммитом работы хендлера
// и удалением джобы, джоба выполнится ещё раз. Поэтому хендлеры обязаны быть
// идемпотентными.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB — то, что есть и у пула, и у транзакции pgx. Очереди больше не нужно:
// каждый шаг — один запрос.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Job — захваченная джоба в руках хендлера.
type Job struct {
	ID      int64
	Kind    string
	Payload json.RawMessage
	// Attempt — номер текущей попытки, с единицы
	Attempt int
}

// Handler выполняет джобу одного kind. Ошибка — повтор с backoff, после
// исчерпания попыток джоба остаётся мёртвой с last_error.
type Handler func(ctx context.Context, job Job) error

// Enqueue ставит джобу, payload кодируется в JSON. db — пул или транзакция
// вызывающего: поставленная в транзакции джоба появится только вместе с её
// коммитом, а откат унесёт её вместе с остальными изменениями. Ради этого
// очередь и живёт в Postgres (ADR-003).
func Enqueue(ctx context.Context, db DB, kind string, payload any) (int64, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("jobs: payload %s: %w", kind, err)
	}
	var id int64
	err = db.QueryRow(ctx,
		`INSERT INTO jobs (kind, payload) VALUES ($1, $2) RETURNING id`,
		kind, body).Scan(&id)
	return id, err
}

// EnqueueOnce ставит джобу не позже чем через delay, если такая же (kind
// и payload) ещё ждёт очереди, — её и подтягивает к этому сроку, а вторую
// не ставит. Так частые события (сохранения доски, S3-6) сводятся к одной
// джобе на интервал. Взятая в работу джоба не в счёт: событие после её
// захвата ставит следующую. Ретрай не подтягивается — его backoff держит
// паузу для упавшего внешнего сервиса.
//
// Две параллельные вставки одной джобы не исключены; хендлеры и так
// идемпотентны, а вызывающий может сериализовать их своей блокировкой.
func EnqueueOnce(ctx context.Context, db DB, kind string, payload any, delay time.Duration) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("jobs: payload %s: %w", kind, err)
	}
	_, err = db.Exec(ctx,
		`WITH waiting AS (
		     UPDATE jobs SET run_at = CASE WHEN attempts = 0
		                                  THEN least(run_at, now() + make_interval(secs => $3))
		                                  ELSE run_at END
		     WHERE kind = $1 AND payload = $2::jsonb AND locked_at IS NULL AND failed_at IS NULL
		     RETURNING id)
		 INSERT INTO jobs (kind, payload, run_at)
		 SELECT $1, $2::jsonb, now() + make_interval(secs => $3)
		 WHERE NOT EXISTS (SELECT 1 FROM waiting)`,
		kind, body, delay.Seconds())
	return err
}
