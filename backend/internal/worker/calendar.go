package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/IEZhu/class/backend/internal/gcal"
	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/store"
)

// calendarSync — событие урока в основном календаре преподавателя
// (S1-1, ADR-023). Идемпотентно: id события назначен до вызова Google
// (lessons.gcal_event_id), поэтому повтор insert получает 409 и становится
// update, а удаление уже удалённого события — успех.
//
// Без рабочего токена (не подключён, не расшифровывается, отозван) создание
// и перенос пропускаются: новое подключение синхронизирует будущие уроки
// заново. Удаление так не восстановить — урока уже нет, — поэтому оно ждёт
// в очереди (store.SetGoogleToken возвращает его в работу).
type calendarSync struct {
	st  *store.Store
	cal *gcal.Client
	// publicBaseURL — https://<DOMAIN>: из него ссылка на урок в описании
	publicBaseURL string
}

func (s calendarSync) handle(ctx context.Context, job jobs.Job) error {
	var p store.CalendarSync
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("payload: %w", err)
	}
	if p.EventID != "" {
		return s.remove(ctx, p)
	}
	if s.cal == nil {
		log.Printf("worker: calendar_sync: урок %d пропущен: %s", p.LessonID, errNotConfigured)
		return nil
	}
	return s.upsert(ctx, p.LessonID)
}

// remove — отмена: урока уже нет, всё нужное — в payload. Без рабочего
// токена ошибка держит джобу в очереди: повторы с backoff, затем мёртвая
// с last_error, пока новое подключение не вернёт её в работу.
func (s calendarSync) remove(ctx context.Context, p store.CalendarSync) error {
	err := s.withCalendar(ctx, p.TeacherID, func(c *gcal.Calendar) error {
		return ignoreEventGone(c.Delete(ctx, p.EventID))
	})
	if errors.As(err, new(noCalendar)) {
		return fmt.Errorf("событие %s отменённого урока %d ждёт повторного подключения календаря: %w", p.EventID, p.LessonID, err)
	}
	return err
}

// upsert — создание и перенос: событие собирается из текущего состояния
// урока, поэтому порядок джоб и их повторы на результат не влияют.
func (s calendarSync) upsert(ctx context.Context, lessonID int64) error {
	l, err := s.st.CalendarLessonByID(ctx, lessonID)
	if errors.Is(err, store.ErrNotFound) {
		// Урок отменён раньше, чем дошла очередь; событие, если оно
		// успело появиться, удалит джоба отмены
		return nil
	}
	if err != nil {
		return err
	}
	ev := gcal.Event{
		ID:          l.EventID,
		Summary:     l.Title,
		Description: s.publicBaseURL + "/lesson/" + strconv.FormatInt(l.ID, 10),
		Start:       l.StartsAt,
		End:         l.EndsAt,
		Attendees:   l.Attendees,
	}
	err = s.withCalendar(ctx, l.TeacherID, func(c *gcal.Calendar) error {
		err := c.Insert(ctx, ev)
		if errors.Is(err, gcal.ErrConflict) {
			// Событие с этим id уже есть: это перенос или прошлая попытка
			// дошла до Google, а ответ — нет
			err = c.Update(ctx, ev)
		}
		if err != nil {
			return err
		}
		// Отмена могла пройти, пока шёл вызов, и её джоба не нашла события
		// (параллельный worker): тогда убираем его сами
		exists, err := s.st.LessonExists(ctx, l.ID)
		if err != nil || exists {
			return err
		}
		return ignoreEventGone(c.Delete(ctx, ev.ID))
	})
	if errors.As(err, new(noCalendar)) {
		log.Printf("worker: calendar_sync: урок %d пропущен: %v", l.ID, err)
		return nil
	}
	return err
}

// noCalendar — у преподавателя нет рабочего токена: повтор джобы тут не
// поможет, нужен новый токен — подключение заново.
type noCalendar struct{ reason string }

func (e noCalendar) Error() string { return e.reason }

var errNotConfigured = noCalendar{"Google Calendar не настроен (GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, GOOGLE_TOKEN_KEY)"}

// withCalendar вызывает fn от имени преподавателя; noCalendar — ключей
// нет, календарь не подключён, токен не расшифровался или отозван.
func (s calendarSync) withCalendar(ctx context.Context, teacherID int64, fn func(*gcal.Calendar) error) error {
	if s.cal == nil {
		return errNotConfigured
	}
	sealed, err := s.st.GoogleToken(ctx, teacherID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && sealed == nil) {
		return noCalendar{fmt.Sprintf("у преподавателя %d не подключён Google Calendar", teacherID)}
	}
	if err != nil {
		return err
	}
	token, err := s.cal.Open(teacherID, sealed)
	if err != nil {
		return noCalendar{fmt.Sprintf("токен преподавателя %d не расшифровывается (сменился GOOGLE_TOKEN_KEY?), нужно подключить календарь заново: %v", teacherID, err)}
	}
	err = fn(s.cal.Calendar(ctx, token))
	if errors.Is(err, gcal.ErrRevoked) {
		// Токен стираем, чтобы /account показал «не подключён». Только этот
		// шифртекст: если преподаватель уже подключился заново, свежий
		// токен остаётся
		if err := s.st.ClearGoogleToken(ctx, teacherID, sealed); err != nil {
			return err
		}
		return noCalendar{fmt.Sprintf("преподаватель %d отозвал доступ к календарю или токен истёк, подключение снято", teacherID)}
	}
	return err
}

// ignoreEventGone — удалять нечего: события нет или оно уже удалено.
func ignoreEventGone(err error) error {
	if errors.Is(err, gcal.ErrNotFound) {
		return nil
	}
	return err
}
