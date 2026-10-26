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
	if s.cal == nil {
		log.Printf("worker: calendar_sync: урок %d пропущен: Google Calendar не настроен (GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, GOOGLE_TOKEN_KEY)", p.LessonID)
		return nil
	}
	if p.EventID != "" {
		// Отмена: урока уже нет, всё нужное — в payload
		return s.withCalendar(ctx, p.TeacherID, p.LessonID, func(c *gcal.Calendar) error {
			return ignoreEventGone(c.Delete(ctx, p.EventID))
		})
	}
	return s.upsert(ctx, p.LessonID)
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
	return s.withCalendar(ctx, l.TeacherID, l.ID, func(c *gcal.Calendar) error {
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
}

// withCalendar вызывает fn от имени преподавателя. Не подключён,
// токен не расшифровался или отозван — урок пропускается со строкой
// в журнале: повтор джобы тут не поможет, а при новом подключении
// будущие уроки синхронизируются заново (store.SetGoogleToken).
func (s calendarSync) withCalendar(ctx context.Context, teacherID, lessonID int64, fn func(*gcal.Calendar) error) error {
	sealed, err := s.st.GoogleToken(ctx, teacherID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && sealed == nil) {
		log.Printf("worker: calendar_sync: урок %d пропущен: у преподавателя %d не подключён Google Calendar", lessonID, teacherID)
		return nil
	}
	if err != nil {
		return err
	}
	token, err := s.cal.Open(teacherID, sealed)
	if err != nil {
		log.Printf("worker: calendar_sync: урок %d пропущен: токен преподавателя %d не расшифровывается (сменился GOOGLE_TOKEN_KEY?), нужно подключить календарь заново: %v",
			lessonID, teacherID, err)
		return nil
	}
	err = fn(s.cal.Calendar(ctx, token))
	if errors.Is(err, gcal.ErrRevoked) {
		// Токен стираем, чтобы /account показал «не подключён»
		if err := s.st.ClearGoogleToken(ctx, teacherID, sealed); err != nil {
			return err
		}
		log.Printf("worker: calendar_sync: урок %d пропущен: преподаватель %d отозвал доступ к календарю или токен истёк, подключение снято", lessonID, teacherID)
		return nil
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
