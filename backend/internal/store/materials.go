package store

import (
	"context"
	"time"
)

type Material struct {
	ID        int64
	LessonID  int64
	Kind      string
	Title     string
	BodyMD    string
	S3Key     *string // файлы в S3 — этап 1 (долг D-4)
	CreatedAt time.Time
}

func (s *Store) CreateMaterial(ctx context.Context, lessonID int64, kind, title, bodyMD string) (*Material, error) {
	m := &Material{LessonID: lessonID, Kind: kind, Title: title, BodyMD: bodyMD}
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO materials (lesson_id, kind, title, body_md)
		 VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		lessonID, kind, title, bodyMD).Scan(&m.ID, &m.CreatedAt); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) MaterialsByLesson(ctx context.Context, lessonID int64) ([]Material, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, lesson_id, kind, title, body_md, s3_key, created_at
		 FROM materials WHERE lesson_id = $1 ORDER BY id`, lessonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Material{}
	for rows.Next() {
		m := Material{}
		if err := rows.Scan(&m.ID, &m.LessonID, &m.Kind, &m.Title, &m.BodyMD, &m.S3Key, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
