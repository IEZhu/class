package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
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
