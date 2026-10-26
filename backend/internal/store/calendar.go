package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
)

// JobCalendarSync — событие урока в Google Calendar организатора (S1-1,
// ADR-023): создание и перенос — insert/update, отмена — delete.
const JobCalendarSync = "calendar_sync"

// CalendarSync — payload джобы JobCalendarSync. Отмена удаляет строку
// урока, поэтому её джоба несёт всё, что нужно для удаления события:
// чей это календарь и какой id у события.
type CalendarSync struct {
	LessonID  int64  `json:"lesson_id"`
	TeacherID int64  `json:"teacher_id,omitempty"`
	EventID   string `json:"event_id,omitempty"`
}

// newEventID — id события в Google назначаем сами: 32 hex-символа из
// gen_random_uuid() укладываются в алфавит base32hex, который требует
// Calendar API (a–v, 0–9, длина от 5).
const newEventID = `replace(gen_random_uuid()::text, '-', '')`

// calendarConnected — подключён ли Google у преподавателя. Транзакция,
// меняющая урок, спрашивает это первой, до блокировки строки урока:
// FOR SHARE встаёт в очередь за параллельным подключением, а порядок
// блокировок тот же, что у SetGoogleToken (users, затем lessons), — без
// взаимной блокировки урок либо увидит новый токен, либо попадёт в
// выборку подключения.
func calendarConnected(ctx context.Context, tx pgx.Tx, teacherID int64) (bool, error) {
	var connected bool
	err := tx.QueryRow(ctx,
		`SELECT google_refresh_token IS NOT NULL FROM users WHERE id = $1 FOR SHARE`,
		teacherID).Scan(&connected)
	return connected, err
}

// queueCalendarSync — id будущего события и джоба calendar_sync в той же
// транзакции, что меняет урок; только при подключённом календаре
// (calendarConnected), иначе синхронизировать некуда.
func queueCalendarSync(ctx context.Context, tx pgx.Tx, lessonID int64) error {
	if _, err := tx.Exec(ctx,
		`UPDATE lessons SET gcal_event_id = `+newEventID+`
		 WHERE id = $1 AND gcal_event_id IS NULL`, lessonID); err != nil {
		return err
	}
	_, err := jobs.Enqueue(ctx, tx, JobCalendarSync, CalendarSync{LessonID: lessonID})
	return err
}

// SetGoogleToken — календарь подключён: шифртекст refresh token и джобы
// calendar_sync для будущих уроков преподавателя одной транзакцией, чтобы
// уроки, заведённые до подключения, тоже появились в календаре. Повторное
// подключение (в том числе другого аккаунта) синхронизирует их ещё раз —
// джоба идемпотентна. ErrNotFound — учётки нет или это не преподаватель.
func (s *Store) SetGoogleToken(ctx context.Context, teacherID int64, sealed []byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE users SET google_refresh_token = $2 WHERE id = $1 AND role = $3`,
		teacherID, sealed, RoleTeacher)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	rows, err := tx.Query(ctx,
		`UPDATE lessons SET gcal_event_id = COALESCE(gcal_event_id, `+newEventID+`)
		 WHERE teacher_id = $1 AND status = 'scheduled' AND ends_at > now()
		 RETURNING id`, teacherID)
	if err != nil {
		return err
	}
	lessonIDs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range lessonIDs {
		if _, err := jobs.Enqueue(ctx, tx, JobCalendarSync, CalendarSync{LessonID: id}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GoogleToken — шифртекст refresh token; nil — календарь не подключён.
func (s *Store) GoogleToken(ctx context.Context, userID int64) ([]byte, error) {
	var sealed []byte
	err := s.pool.QueryRow(ctx,
		`SELECT google_refresh_token FROM users WHERE id = $1`, userID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sealed, err
}

// DisconnectGoogle стирает токен и возвращает прежний шифртекст (nil —
// подключения не было): api отзывает его в Google уже после удаления.
func (s *Store) DisconnectGoogle(ctx context.Context, userID int64) ([]byte, error) {
	var sealed []byte
	err := s.pool.QueryRow(ctx,
		`UPDATE users u SET google_refresh_token = NULL
		 FROM (SELECT id, google_refresh_token FROM users WHERE id = $1 FOR UPDATE) old
		 WHERE u.id = old.id
		 RETURNING old.google_refresh_token`, userID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sealed, err
}

// ClearGoogleToken стирает именно этот шифртекст: если преподаватель
// успел подключиться заново, свежий токен не трогаем.
func (s *Store) ClearGoogleToken(ctx context.Context, userID int64, sealed []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET google_refresh_token = NULL
		 WHERE id = $1 AND google_refresh_token = $2`, userID, sealed)
	return err
}

// CalendarLesson — то, из чего worker собирает событие урока.
type CalendarLesson struct {
	ID        int64
	TeacherID int64
	EventID   string
	// Title — название события или имя группы (как web/lib/lesson-title.ts)
	Title     string
	StartsAt  time.Time
	EndsAt    time.Time
	Attendees []string
}

// CalendarLessonByID — урок для calendar_sync. Участники — снапшот
// lesson_participants без гостей (у них синтетический email,
// ADR-010) и без самого преподавателя: событие и так в его календаре.
// ErrNotFound — урок отменён.
func (s *Store) CalendarLessonByID(ctx context.Context, lessonID int64) (*CalendarLesson, error) {
	l := &CalendarLesson{ID: lessonID}
	var eventID *string
	err := s.pool.QueryRow(ctx,
		`SELECT l.teacher_id, l.gcal_event_id, COALESCE(l.title, g.name, ''), l.starts_at, l.ends_at
		 FROM lessons l LEFT JOIN groups g ON g.id = l.group_id
		 WHERE l.id = $1`, lessonID).Scan(&l.TeacherID, &eventID, &l.Title, &l.StartsAt, &l.EndsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if eventID == nil {
		// queueCalendarSync назначает id в той же транзакции, что ставит джобу
		return nil, errors.New("store: у урока в calendar_sync нет gcal_event_id")
	}
	l.EventID = *eventID
	rows, err := s.pool.Query(ctx,
		`SELECT u.email FROM lesson_participants lp JOIN users u ON u.id = lp.user_id
		 WHERE lp.lesson_id = $1 AND u.role <> $2 AND u.id <> $3
		 ORDER BY u.id`, lessonID, RoleGuest, l.TeacherID)
	if err != nil {
		return nil, err
	}
	if l.Attendees, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, err
	}
	return l, nil
}

// LessonExists — урок ещё не отменён.
func (s *Store) LessonExists(ctx context.Context, lessonID int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lessons WHERE id = $1)`, lessonID).Scan(&ok)
	return ok, err
}
