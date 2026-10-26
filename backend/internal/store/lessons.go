package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
)

type Lesson struct {
	ID int64
	// GroupID пуст у открытого события: туда приходят по ссылке,
	// а не составом группы (ADR-010).
	GroupID   *int64
	Title     string
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
// из текущего состава группы (lesson_participants — они же приглашённые
// в событие календаря) и ставит calendar_sync (S1-1).
// Без группы это открытое событие (ADR-010): снапшотить некого, участники
// появятся по гостевой ссылке.
func (s *Store) CreateLesson(ctx context.Context, groupID *int64, title string, teacherID int64, startsAt, endsAt time.Time) (*Lesson, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	calendar, err := calendarConnected(ctx, tx, teacherID)
	if err != nil {
		return nil, err
	}
	l := &Lesson{GroupID: groupID, Title: title, TeacherID: teacherID, StartsAt: startsAt, EndsAt: endsAt}
	if err := tx.QueryRow(ctx,
		`INSERT INTO lessons (group_id, title, teacher_id, starts_at, ends_at)
		 VALUES ($1, NULLIF($2, ''), $3, $4, $5) RETURNING id, status`,
		groupID, title, teacherID, startsAt, endsAt).Scan(&l.ID, &l.Status); err != nil {
		return nil, err
	}
	if groupID != nil {
		// Учётки в архиве (ADR-024) в новые уроки не попадают
		if _, err := tx.Exec(ctx,
			`INSERT INTO lesson_participants (lesson_id, user_id)
			 SELECT $1, gm.user_id FROM group_members gm
			 JOIN users u ON u.id = gm.user_id
			 WHERE gm.group_id = $2 AND u.deactivated_at IS NULL`,
			l.ID, *groupID); err != nil {
			return nil, err
		}
	}
	if calendar {
		if err := queueCalendarSync(ctx, tx, l.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

// ListLessonsForUser: teacher видит свои уроки, student — уроки, где он
// в снапшоте участников (lesson_participants).
func (s *Store) ListLessonsForUser(ctx context.Context, u *User) ([]LessonListItem, error) {
	// LEFT JOIN: у открытого события группы нет (ADR-010)
	query := `SELECT l.id, l.group_id, COALESCE(l.title, ''), l.teacher_id,
	        l.starts_at, l.ends_at, l.status, COALESCE(g.name, '')
		 FROM lessons l LEFT JOIN groups g ON g.id = l.group_id
		 WHERE l.teacher_id = $1 ORDER BY l.starts_at`
	if u.Role != "teacher" {
		query = `SELECT l.id, l.group_id, COALESCE(l.title, ''), l.teacher_id,
		        l.starts_at, l.ends_at, l.status, COALESCE(g.name, '')
		 FROM lessons l
		 LEFT JOIN groups g ON g.id = l.group_id
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
		if err := rows.Scan(&it.ID, &it.GroupID, &it.Title, &it.TeacherID, &it.StartsAt, &it.EndsAt, &it.Status, &it.GroupName); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) GetLessonDetail(ctx context.Context, lessonID int64) (*LessonDetail, error) {
	d := &LessonDetail{}
	err := s.pool.QueryRow(ctx,
		`SELECT l.id, l.group_id, COALESCE(l.title, ''), l.teacher_id,
		        l.starts_at, l.ends_at, l.status, COALESCE(g.name, ''), t.name
		 FROM lessons l
		 LEFT JOIN groups g ON g.id = l.group_id
		 JOIN users t ON t.id = l.teacher_id
		 WHERE l.id = $1`, lessonID).
		Scan(&d.ID, &d.GroupID, &d.Title, &d.TeacherID, &d.StartsAt, &d.EndsAt, &d.Status, &d.GroupName, &d.TeacherName)
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
		`SELECT u.id, u.email, u.name, u.role, u.deactivated_at IS NOT NULL
		 FROM lesson_participants lp JOIN users u ON u.id = lp.user_id
		 WHERE lp.lesson_id = $1 ORDER BY u.id`, lessonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d.Participants = []GroupMember{}
	for rows.Next() {
		m := GroupMember{}
		if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.Archived); err != nil {
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

// LessonTeacherAndGroup — владелец урока и его группа (nil у открытого
// события): гостевой ссылкой распоряжаются только у событий (ADR-010).
func (s *Store) LessonTeacherAndGroup(ctx context.Context, lessonID int64) (int64, *int64, error) {
	var teacherID int64
	var groupID *int64
	err := s.pool.QueryRow(ctx,
		`SELECT teacher_id, group_id FROM lessons WHERE id = $1`, lessonID).Scan(&teacherID, &groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	return teacherID, groupID, err
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

	calendar, err := calendarConnected(ctx, tx, teacherID)
	if err != nil {
		return nil, err
	}
	if err := lessonForUpdate(ctx, tx, lessonID, teacherID); err != nil {
		return nil, err
	}
	l := &Lesson{ID: lessonID, TeacherID: teacherID}
	if err := tx.QueryRow(ctx,
		`UPDATE lessons SET starts_at = $2, ends_at = $3 WHERE id = $1
		 RETURNING group_id, COALESCE(title, ''), starts_at, ends_at, status`,
		lessonID, startsAt, endsAt).Scan(&l.GroupID, &l.Title, &l.StartsAt, &l.EndsAt, &l.Status); err != nil {
		return nil, err
	}
	if calendar {
		if err := queueCalendarSync(ctx, tx, lessonID); err != nil {
			return nil, err
		}
	}
	return l, tx.Commit(ctx)
}

// CancelLesson удаляет запланированный урок (материалы и участники — каскадом).
// Отдельного статуса cancelled в статус-машине нет (03-data-model.md).
// Событие в календаре удаляет джоба calendar_sync: строки урока к её
// запуску уже нет, поэтому id события и организатор уходят в payload.
// Возвращает ключи файлов удалённых материалов: объекты в хранилище
// каскад не достаёт, их удаляет вызывающий (ADR-024).
func (s *Store) CancelLesson(ctx context.Context, lessonID, teacherID int64) ([]string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lessonForUpdate(ctx, tx, lessonID, teacherID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx,
		`DELETE FROM materials WHERE lesson_id = $1 AND s3_key IS NOT NULL RETURNING s3_key`, lessonID)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	var eventID *string
	if err := tx.QueryRow(ctx,
		`DELETE FROM lessons WHERE id = $1 RETURNING gcal_event_id`, lessonID).Scan(&eventID); err != nil {
		return nil, err
	}
	// Без id событие в календарь не ставилось: удалять нечего. С id
	// удаление ставится и без подключения: событие могло появиться раньше,
	// и джоба дождётся нового токена (ADR-023)
	if eventID != nil {
		if _, err := jobs.Enqueue(ctx, tx, JobCalendarSync,
			CalendarSync{LessonID: lessonID, TeacherID: teacherID, EventID: *eventID}); err != nil {
			return nil, err
		}
	}
	return keys, tx.Commit(ctx)
}
