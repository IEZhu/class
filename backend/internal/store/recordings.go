package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// StartLessonRecording переводит урок scheduled → live и в той же транзакции
// вызывает start — запуск Egress. Переход фиксируется только вместе
// с успешным запуском: если start вернул ошибку, транзакция откатывается,
// урок остаётся scheduled и следующая доставка вебхука повторит запуск
// (ADR-012). Иначе сорванный старт навсегда оставлял бы урок без записи.
//
// SKIP LOCKED делает вызов идемпотентным и неблокирующим: пока один запуск
// держит строку урока, параллельная доставка room_started не ждёт его
// и не запускает второй Egress. ErrNotFound — урока нет, он уже не
// scheduled или запуск идёт прямо сейчас, то есть запускать нечего.
func (s *Store) StartLessonRecording(ctx context.Context, lessonID int64, start func(context.Context) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	err = tx.QueryRow(ctx,
		`UPDATE lessons SET status = 'live'
		 WHERE id = (SELECT id FROM lessons
		             WHERE id = $1 AND status = 'scheduled'
		             FOR UPDATE SKIP LOCKED)
		 RETURNING id`, lessonID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := start(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FinishLessonRecording — запись готова: путь в бакете и статус.
// Статус приходит параметром, потому что с появлением ASR (S2-1) урок
// после egress_ended будет уходить в processing, а не сразу в done.
func (s *Store) FinishLessonRecording(ctx context.Context, lessonID int64, s3Key, status string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE lessons SET recording_s3_key = $2, status = $3 WHERE id = $1`,
		lessonID, s3Key, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// LessonRecordingKey — путь записи урока, пусто если записи ещё нет.
func (s *Store) LessonRecordingKey(ctx context.Context, lessonID int64) (string, error) {
	var key *string
	err := s.pool.QueryRow(ctx,
		`SELECT recording_s3_key FROM lessons WHERE id = $1`, lessonID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if key == nil {
		return "", nil
	}
	return *key, nil
}
