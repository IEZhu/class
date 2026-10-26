package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
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

// JobRecordingReady — джоба-плейсхолдер точки вставки ASR (S1-5, ADR-013):
// запись принята, урок в processing. Пока транскрибации нет, worker сразу
// переводит урок в done; S2-1 заменит её джобой asr_submit.
const JobRecordingReady = "recording_ready"

// RecordingReady — payload джобы JobRecordingReady.
type RecordingReady struct {
	LessonID int64 `json:"lesson_id"`
}

// FinishLessonRecording — запись готова: путь в бакете, статус processing
// и джоба JobRecordingReady одной транзакцией — урок не застрянет
// в processing без джобы, которая его оттуда выведет. Условие на статус
// делает вызов идемпотентным: повторная доставка egress_ended не поставит
// вторую джобу и не вернёт done-урок в processing. ErrNotFound — урока нет
// либо запись уже принята.
func (s *Store) FinishLessonRecording(ctx context.Context, lessonID int64, s3Key string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE lessons SET recording_s3_key = $2, status = 'processing'
		 WHERE id = $1 AND status IN ('scheduled', 'live')`,
		lessonID, s3Key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := jobs.Enqueue(ctx, tx, JobRecordingReady, RecordingReady{LessonID: lessonID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FinishLessonProcessing — обработка записи завершена, урок done. Повторный
// вызов (at-least-once доставка джобы) ничего не меняет.
func (s *Store) FinishLessonProcessing(ctx context.Context, lessonID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE lessons SET status = 'done' WHERE id = $1 AND status = 'processing'`, lessonID)
	return err
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
