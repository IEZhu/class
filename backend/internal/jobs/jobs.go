// Package jobs — очередь фоновых задач в Postgres (ADR-003, ADR-012):
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
