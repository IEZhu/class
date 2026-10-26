package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
)

// StartLessonRecording помечает урок начавшимся. Условие status = 'scheduled'
// делает вызов идемпотентным: LiveKit может доставить room_started повторно,
// и вторая доставка не запустит второй Egress. ErrNotFound — урока нет либо
// он уже не scheduled, то есть запускать нечего.
func (s *Store) StartLessonRecording(ctx context.Context, lessonID int64) error {
	var id int64
	err := s.pool.QueryRow(ctx,
		`UPDATE lessons SET status = 'live'
		 WHERE id = $1 AND status = 'scheduled'
		 RETURNING id`, lessonID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// JobRecordingReady — джоба-плейсхолдер точки вставки ASR (S1-5, ADR-012):
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
