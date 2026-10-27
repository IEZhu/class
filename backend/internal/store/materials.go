package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
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
	// ETag объекта на постоянном ключе, записанный при подтверждении
	ETag string
}

// Ready — загрузка подтверждена, файл можно отдавать участникам.
func (f *MaterialFile) Ready() bool { return f != nil && f.UploadedAt != nil }

const materialColumns = `id, lesson_id, kind, title, body_md, created_at,
	s3_key, file_name, file_content_type, file_size, file_uploaded_at, COALESCE(file_etag, '')`

// scanMaterial читает materialColumns; extra — колонки после них.
func scanMaterial(row pgx.Row, extra ...any) (*Material, error) {
	m := &Material{}
	var (
		key, name, contentType *string
		size                   *int64
		uploadedAt             *time.Time
		etag                   string
	)
	dest := append([]any{&m.ID, &m.LessonID, &m.Kind, &m.Title, &m.BodyMD, &m.CreatedAt,
		&key, &name, &contentType, &size, &uploadedAt, &etag}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	// Колонки файла заполнены целиком или пусты — держит CHECK
	// materials_file_complete (0013)
	if key != nil {
		m.File = &MaterialFile{S3Key: *key, Name: *name, ContentType: *contentType, Size: *size,
			UploadedAt: uploadedAt, ETag: etag}
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

// ConfirmMaterialFile отмечает загрузку подтверждённой и запоминает ETag
// объекта. Повтор не сдвигает ни момента первой отметки, ни её ETag.
func (s *Store) ConfirmMaterialFile(ctx context.Context, id int64, etag string) (*Material, error) {
	m, err := scanMaterial(s.pool.QueryRow(ctx,
		`UPDATE materials SET file_uploaded_at = COALESCE(file_uploaded_at, now()),
		                      file_etag = COALESCE(file_etag, $2)
		 WHERE id = $1 AND s3_key IS NOT NULL RETURNING `+materialColumns, id, etag))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// JobMaterialFilesDelete — удалить объекты файлов материалов, строк
// которых уже нет (ADR-024): отмена урока и удаление материала, когда
// хранилище не ответило. Удаление отсутствующего ключа — не ошибка,
// поэтому повтор джобы безопасен.
const JobMaterialFilesDelete = "material_files_delete"

// MaterialFilesDelete — payload джобы JobMaterialFilesDelete: постоянные
// ключи файлов; копии под materials-pending/ worker выводит из них сам.
type MaterialFilesDelete struct {
	Keys []string `json:"keys"`
}

// DeleteMaterial — удалить материал. Объект в хранилище удаляет
// вызывающий; leftKeys — ключи, которые ему удалить не удалось: их уберёт
// джоба JobMaterialFilesDelete, поставленная той же транзакцией.
func (s *Store) DeleteMaterial(ctx context.Context, id int64, leftKeys []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `DELETE FROM materials WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := queueMaterialFilesDelete(ctx, tx, leftKeys); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// QueueMaterialFilesDelete — поставить уборку объектов, на которые строк
// уже нет, вне транзакции вызывающего.
func (s *Store) QueueMaterialFilesDelete(ctx context.Context, keys []string) error {
	return queueMaterialFilesDelete(ctx, s.pool, keys)
}

func queueMaterialFilesDelete(ctx context.Context, db jobs.DB, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := jobs.Enqueue(ctx, db, JobMaterialFilesDelete, MaterialFilesDelete{Keys: keys})
	return err
}
