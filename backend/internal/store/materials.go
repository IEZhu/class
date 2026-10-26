package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Material struct {
	ID        int64
	LessonID  int64
	Kind      string
	Title     string
	BodyMD    string
	File      *MaterialFile // nil у текстового материала
	CreatedAt time.Time
}

// MaterialFile — файл материала в хранилище (долг D-4, ADR-024). Ключ
// и ограничения задаёт api; браузер грузит файл напрямую по presigned PUT.
type MaterialFile struct {
	S3Key       string
	Name        string
	ContentType string
	Size        int64
	// UploadedAt пуст, пока api не нашёл объект в хранилище и не сверил
	// его размер: такой файл видит только преподаватель урока.
	UploadedAt *time.Time
}

// Ready — загрузка подтверждена, файл можно отдавать участникам.
func (f *MaterialFile) Ready() bool { return f != nil && f.UploadedAt != nil }

const materialColumns = `id, lesson_id, kind, title, body_md, created_at,
	s3_key, file_name, file_content_type, file_size, file_uploaded_at`

// scanMaterial читает materialColumns; extra — колонки после них.
func scanMaterial(row pgx.Row, extra ...any) (*Material, error) {
	m := &Material{}
	var (
		key, name, contentType *string
		size                   *int64
		uploadedAt             *time.Time
	)
	dest := append([]any{&m.ID, &m.LessonID, &m.Kind, &m.Title, &m.BodyMD, &m.CreatedAt,
		&key, &name, &contentType, &size, &uploadedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	// Колонки файла заполнены целиком или пусты — держит CHECK
	// materials_file_complete (0013)
	if key != nil {
		m.File = &MaterialFile{S3Key: *key, Name: *name, ContentType: *contentType, Size: *size, UploadedAt: uploadedAt}
	}
	return m, nil
}

// CreateMaterial — материал или домашка; file != nil заводит его вместе
// с файлом, ожидающим загрузки.
func (s *Store) CreateMaterial(ctx context.Context, lessonID int64, kind, title, bodyMD string, file *MaterialFile) (*Material, error) {
	var key, name, contentType *string
	var size *int64
	if file != nil {
		key, name, contentType, size = &file.S3Key, &file.Name, &file.ContentType, &file.Size
	}
	return scanMaterial(s.pool.QueryRow(ctx,
		`INSERT INTO materials (lesson_id, kind, title, body_md, s3_key, file_name, file_content_type, file_size)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+materialColumns,
		lessonID, kind, title, bodyMD, key, name, contentType, size))
}

func (s *Store) MaterialsByLesson(ctx context.Context, lessonID int64) ([]Material, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+materialColumns+` FROM materials WHERE lesson_id = $1 ORDER BY id`, lessonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Material{}
	for rows.Next() {
		m, err := scanMaterial(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// MaterialWithTeacher — материал и преподаватель его урока: по нему
// проверяются права на файл без загрузки полной детали урока.
func (s *Store) MaterialWithTeacher(ctx context.Context, id int64) (*Material, int64, error) {
	var teacherID int64
	m, err := scanMaterial(s.pool.QueryRow(ctx,
		`SELECT `+materialColumns+`, (SELECT l.teacher_id FROM lessons l WHERE l.id = materials.lesson_id)
		 FROM materials WHERE id = $1`, id), &teacherID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	return m, teacherID, nil
}

// ConfirmMaterialFile отмечает загрузку подтверждённой. Повтор не сдвигает
// момент первой отметки.
func (s *Store) ConfirmMaterialFile(ctx context.Context, id int64) (*Material, error) {
	m, err := scanMaterial(s.pool.QueryRow(ctx,
		`UPDATE materials SET file_uploaded_at = COALESCE(file_uploaded_at, now())
		 WHERE id = $1 AND s3_key IS NOT NULL RETURNING `+materialColumns, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// DeleteMaterial — удалить материал; объект в хранилище удаляет вызывающий.
func (s *Store) DeleteMaterial(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM materials WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
