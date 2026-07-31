package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Lesson struct {
	ID        int64
	GroupID   int64
	TeacherID int64
	StartsAt  time.Time
	EndsAt    time.Time
	Status    string
}

type LessonListItem struct {
	Lesson
	GroupName string
}

type LessonDetail struct {
	Lesson
	GroupName    string
	TeacherName  string
	Materials    []Material
	Participants []GroupMember
}

// CreateLesson создаёт урок и в той же транзакции снапшотит участников
// из текущего состава группы (lesson_participants; нужен и календарю S1-2).
func (s *Store) CreateLesson(ctx context.Context, groupID, teacherID int64, startsAt, endsAt time.Time) (*Lesson, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	l := &Lesson{GroupID: groupID, TeacherID: teacherID, StartsAt: startsAt, EndsAt: endsAt}
	if err := tx.QueryRow(ctx,
		`INSERT INTO lessons (group_id, teacher_id, starts_at, ends_at)
		 VALUES ($1, $2, $3, $4) RETURNING id, status`,
		groupID, teacherID, startsAt, endsAt).Scan(&l.ID, &l.Status); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO lesson_participants (lesson_id, user_id)
		 SELECT $1, user_id FROM group_members WHERE group_id = $2`,
		l.ID, groupID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

// ListLessonsForUser: teacher видит свои уроки, student — уроки, где он
// в снапшоте участников (lesson_participants).
func (s *Store) ListLessonsForUser(ctx context.Context, u *User) ([]LessonListItem, error) {
	query := `SELECT l.id, l.group_id, l.teacher_id, l.starts_at, l.ends_at, l.status, g.name
		 FROM lessons l JOIN groups g ON g.id = l.group_id
		 WHERE l.teacher_id = $1 ORDER BY l.starts_at`
	if u.Role != "teacher" {
		query = `SELECT l.id, l.group_id, l.teacher_id, l.starts_at, l.ends_at, l.status, g.name
		 FROM lessons l
		 JOIN groups g ON g.id = l.group_id
		 JOIN lesson_participants lp ON lp.lesson_id = l.id
		 WHERE lp.user_id = $1 ORDER BY l.starts_at`
	}
	rows, err := s.pool.Query(ctx, query, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []LessonListItem{}
	for rows.Next() {
		it := LessonListItem{}
		if err := rows.Scan(&it.ID, &it.GroupID, &it.TeacherID, &it.StartsAt, &it.EndsAt, &it.Status, &it.GroupName); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) GetLessonDetail(ctx context.Context, lessonID int64) (*LessonDetail, error) {
	d := &LessonDetail{}
	err := s.pool.QueryRow(ctx,
		`SELECT l.id, l.group_id, l.teacher_id, l.starts_at, l.ends_at, l.status, g.name, t.name
		 FROM lessons l
		 JOIN groups g ON g.id = l.group_id
		 JOIN users t ON t.id = l.teacher_id
		 WHERE l.id = $1`, lessonID).
		Scan(&d.ID, &d.GroupID, &d.TeacherID, &d.StartsAt, &d.EndsAt, &d.Status, &d.GroupName, &d.TeacherName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if d.Materials, err = s.MaterialsByLesson(ctx, lessonID); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT u.id, u.email, u.name, u.role
		 FROM lesson_participants lp JOIN users u ON u.id = lp.user_id
		 WHERE lp.lesson_id = $1 ORDER BY u.id`, lessonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d.Participants = []GroupMember{}
	for rows.Next() {
		m := GroupMember{}
		if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.Role); err != nil {
			return nil, err
		}
		d.Participants = append(d.Participants, m)
	}
	return d, rows.Err()
}

// LessonTeacherID — лёгкая выборка владельца урока (для проверок прав
// без загрузки полной детали).
func (s *Store) LessonTeacherID(ctx context.Context, lessonID int64) (int64, error) {
	var teacherID int64
	err := s.pool.QueryRow(ctx,
		`SELECT teacher_id FROM lessons WHERE id = $1`, lessonID).Scan(&teacherID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return teacherID, err
}

// IsLessonParticipant — есть ли пользователь в снапшоте участников урока.
func (s *Store) IsLessonParticipant(ctx context.Context, lessonID, userID int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM lesson_participants WHERE lesson_id = $1 AND user_id = $2)`,
		lessonID, userID).Scan(&ok)
	return ok, err
}

var (
	// ErrLessonNotEditable — урок не в статусе scheduled (перенос/отмена запрещены).
	ErrLessonNotEditable = errors.New("lesson not editable")
	// ErrNotOwner — операция доступна только teacher'у урока.
	ErrNotOwner = errors.New("not lesson owner")
)

// lessonForUpdate — общая проверка для переноса/отмены: существование,
// владелец, статус scheduled. Возвращает типизированные ошибки.
func lessonForUpdate(ctx context.Context, tx pgx.Tx, lessonID, teacherID int64) error {
	var ownerID int64
	var status string
	err := tx.QueryRow(ctx,
		`SELECT teacher_id, status FROM lessons WHERE id = $1 FOR UPDATE`,
		lessonID).Scan(&ownerID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if ownerID != teacherID {
		return ErrNotOwner
	}
	if status != "scheduled" {
		return ErrLessonNotEditable
	}
	return nil
}

func (s *Store) RescheduleLesson(ctx context.Context, lessonID, teacherID int64, startsAt, endsAt time.Time) (*Lesson, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lessonForUpdate(ctx, tx, lessonID, teacherID); err != nil {
		return nil, err
	}
	l := &Lesson{ID: lessonID, TeacherID: teacherID}
	if err := tx.QueryRow(ctx,
		`UPDATE lessons SET starts_at = $2, ends_at = $3 WHERE id = $1
		 RETURNING group_id, starts_at, ends_at, status`,
		lessonID, startsAt, endsAt).Scan(&l.GroupID, &l.StartsAt, &l.EndsAt, &l.Status); err != nil {
		return nil, err
	}
	return l, tx.Commit(ctx)
}

// CancelLesson удаляет запланированный урок (материалы и участники — каскадом).
// Отдельного статуса cancelled в статус-машине нет (03-data-model.md).
func (s *Store) CancelLesson(ctx context.Context, lessonID, teacherID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lessonForUpdate(ctx, tx, lessonID, teacherID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM lessons WHERE id = $1`, lessonID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
