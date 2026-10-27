package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
)

// Запись урока — набор сегментов lesson_recordings (ADR-031): каждый запуск
// Egress пишет свой файл, egress_ended закрывает сегмент, а не урок. Урок
// живёт, пока открыто окно комнаты; после него closeLessonIfOver выбирает
// запись и переводит урок дальше — по egress_ended или уборкой
// SweepRecordings.

const (
	// RoomOpensBefore и RoomClosesAfter — окно комнаты урока (ADR-031):
	// токен в комнату и запуск записи — только с starts_at − RoomOpensBefore
	// до ends_at + RoomClosesAfter. Раньше комната пуста по смыслу, позже —
	// урок кончился; запас после конца — на переподключение затянувшегося урока.
	RoomOpensBefore = 15 * time.Minute
	RoomClosesAfter = 15 * time.Minute
	// recordingLostAfter — сегмент, по которому egress_ended не пришёл
	// за столько после конца урока, считается потерянным: LiveKit сдаётся
	// с доставкой раньше, а урок не должен ждать его вечно.
	recordingLostAfter = 3 * time.Hour
)

// RoomWindow — когда комната урока открыта (RoomOpensBefore, RoomClosesAfter).
func RoomWindow(startsAt, endsAt time.Time) (opens, closes time.Time) {
	return startsAt.Add(-RoomOpensBefore), endsAt.Add(RoomClosesAfter)
}

// seconds — длительность для SQL: `$n::bigint * interval '1 second'`.
func seconds(d time.Duration) int64 { return int64(d / time.Second) }

// StartLessonRecording запускает сегмент записи: в одной транзакции
// захватывает урок, вызывает start — запуск Egress в файл key — и пишет
// сегмент active, урок становится live. Если start вернул ошибку,
// транзакция откатывается, урок остаётся как был и следующая доставка
// вебхука повторит запуск (ADR-012).
//
// Запускать есть что, только пока окно комнаты открыто и урок не ушёл
// в обработку, — ранний вход в будущий урок его не трогает. Сегмент
// на сессию комнаты один: пока в сессии roomSID пишет Egress, запуск не
// нужен; новая сессия (комната закрылась и открылась снова) пишется новым
// сегментом, даже если egress_ended прошлой ещё не дошёл. Пустой sid
// совпадает с любым.
//
// Строка урока захватывается FOR NO KEY UPDATE SKIP LOCKED: параллельная
// доставка room_started не ждёт идущий запуск и второй Egress не запускает,
// а вход гостя (FOR KEY SHARE) захвату не мешает и не ждёт вызова LiveKit.
// ErrNotFound — урока нет, окно закрыто, запись в этой сессии уже идёт или
// запуск идёт прямо сейчас, то есть запускать нечего.
func (s *Store) StartLessonRecording(ctx context.Context, lessonID int64, roomSID, key string, start func(context.Context) (egressID string, err error)) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	err = tx.QueryRow(ctx,
		`SELECT id FROM lessons
		 WHERE id = $1 AND status IN ('scheduled', 'live')
		   AND now() BETWEEN starts_at - $2::bigint * interval '1 second' AND ends_at + $3::bigint * interval '1 second'
		 FOR NO KEY UPDATE SKIP LOCKED`,
		lessonID, seconds(RoomOpensBefore), seconds(RoomClosesAfter)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Отдельный запрос после захвата: он видит сегменты, зафиксированные
	// параллельными транзакциями, пока эта ждала строку
	var recording bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM lesson_recordings
		                WHERE lesson_id = $1 AND status = 'active'
		                  AND (room_sid = $2 OR room_sid = '' OR $2 = ''))`,
		lessonID, roomSID).Scan(&recording); err != nil {
		return err
	}
	if recording {
		return ErrNotFound
	}

	egressID, err := start(ctx)
	if err != nil {
		return err
	}
	// egress_ended мог обогнать фиксацию (Egress закончился за секунды)
	// и уже завести сегмент — тогда он главнее
	if _, err := tx.Exec(ctx,
		`INSERT INTO lesson_recordings (lesson_id, egress_id, room_sid, s3_key)
		 VALUES ($1, $2, $3, $4) ON CONFLICT (egress_id) DO NOTHING`,
		lessonID, egressID, roomSID, key); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE lessons SET status = 'live' WHERE id = $1`, lessonID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordingResult — итог сегмента из egress_ended.
type RecordingResult struct {
	EgressID string
	RoomSID  string
	// Key — файл, который Egress записал; пусто — файла нет
	Key  string
	Size int64
	// Error — непусто, если сегмент не удался: файла нет или он битый
	Error string
}

// EndLessonRecording — Egress завершился: сегмент становится complete или
// failed (Error), и урок, чьё окно уже закрылось, переходит дальше
// (closeLessonIfOver). Сегмент, которого мы не заводили (запуск, чья
// транзакция не зафиксировалась, ADR-012), заводится здесь же. Принятый
// complete-сегмент повтор доставки не трогает.
//
// Всё одной транзакцией: сбой откатывает и сегмент, и переход урока,
// а повтор доставки сделает то же заново. ErrNotFound — урока нет или
// сегмент уже принят, менять нечего.
func (s *Store) EndLessonRecording(ctx context.Context, lessonID int64, res RecordingResult) error {
	status := "complete"
	if res.Error != "" {
		status = "failed"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`INSERT INTO lesson_recordings AS r
		     (lesson_id, egress_id, room_sid, s3_key, status, size_bytes, error, ended_at)
		 SELECT id, $2, $3, NULLIF($4, ''), $5, $6, NULLIF($7, ''), now() FROM lessons WHERE id = $1
		 ON CONFLICT (egress_id) DO UPDATE
		     SET status = excluded.status, s3_key = COALESCE(excluded.s3_key, r.s3_key),
		         size_bytes = excluded.size_bytes, error = excluded.error, ended_at = excluded.ended_at
		     WHERE r.lesson_id = excluded.lesson_id AND r.status <> 'complete'`,
		lessonID, res.EgressID, res.RoomSID, res.Key, status, res.Size, res.Error)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := closeLessonIfOver(ctx, tx, lessonID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// closeLessonIfOver завершает урок, если его окно закрылось и ни один
// сегмент больше не пишется: запись — самый большой complete-сегмент
// (лишние короткие — обрывки до переподключения, ADR-031), урок уходит
// в processing с джобой recording_ready; без записи — сразу done. Урок
// в scheduled с сегментами — тот, чей запуск записи не зафиксировался
// (ADR-012): комната была, запись есть. Возвращает, перешёл ли урок.
//
// Захват SKIP LOCKED: строку держит запуск нового сегмента — значит, урок
// ещё идёт; кого пропустили, доберёт SweepRecordings. Сегменты читаются
// отдельным запросом уже после захвата и видят свежие.
func closeLessonIfOver(ctx context.Context, tx pgx.Tx, lessonID int64) (bool, error) {
	var id int64
	err := tx.QueryRow(ctx,
		`SELECT id FROM lessons l
		 WHERE id = $1 AND now() > ends_at + $2::bigint * interval '1 second'
		   AND (status = 'live' OR (status = 'scheduled'
		        AND EXISTS (SELECT 1 FROM lesson_recordings r WHERE r.lesson_id = l.id)))
		 FOR NO KEY UPDATE SKIP LOCKED`,
		lessonID, seconds(RoomClosesAfter)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var active bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM lesson_recordings WHERE lesson_id = $1 AND status = 'active')`,
		lessonID).Scan(&active); err != nil {
		return false, err
	}
	if active {
		return false, nil
	}

	// Доска закрывается вместе с переходом (whiteboardOpen): сцена уже
	// окончательная, её слова — в кандидаты словаря урока (S3-7)
	if err := scheduleWhiteboardTerms(ctx, tx, lessonID); err != nil {
		return false, err
	}
	var key string
	err = tx.QueryRow(ctx,
		`SELECT s3_key FROM lesson_recordings WHERE lesson_id = $1 AND status = 'complete'
		 ORDER BY size_bytes DESC NULLS LAST, id DESC LIMIT 1`, lessonID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		// Записи нет: плеер ответит no_recording, ASR нечего отдавать
		_, err := tx.Exec(ctx, `UPDATE lessons SET status = 'done' WHERE id = $1`, lessonID)
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	// Точка вставки ASR (S1-5, ADR-013): processing вместе с джобой
	// recording_ready — урок не застрянет в processing без неё
	if _, err := tx.Exec(ctx,
		`UPDATE lessons SET recording_s3_key = $2, status = 'processing' WHERE id = $1`,
		lessonID, key); err != nil {
		return false, err
	}
	if _, err := jobs.Enqueue(ctx, tx, JobRecordingReady, RecordingReady{LessonID: lessonID}); err != nil {
		return false, err
	}
	return true, nil
}

// JobRecordingSweep — периодическая уборка записей (ADR-031), payload {}.
const JobRecordingSweep = "recording_sweep"

// SweepRecordings доводит уроки, которые egress_ended сам не завершил:
// комната опустела до конца урока, запись упала, egress_ended потерялся.
// Сегмент, по которому egress_ended не пришёл за recordingLostAfter после
// конца урока, помечается failed; затем каждый урок с закрытым окном
// проходит closeLessonIfOver в своей транзакции — сбой одного не держит
// остальных. Повтор безопасен: переходы условные. Возвращает число
// завершённых уроков.
func (s *Store) SweepRecordings(ctx context.Context) (int, error) {
	if _, err := s.pool.Exec(ctx,
		`UPDATE lesson_recordings r
		 SET status = 'failed', error = 'egress_ended не пришёл', ended_at = now()
		 FROM lessons l
		 WHERE l.id = r.lesson_id AND r.status = 'active'
		   AND now() > l.ends_at + $1::bigint * interval '1 second'`,
		seconds(recordingLostAfter)); err != nil {
		return 0, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM lessons l
		 WHERE now() > ends_at + $1::bigint * interval '1 second'
		   AND (status = 'live' OR (status = 'scheduled'
		        AND EXISTS (SELECT 1 FROM lesson_recordings r WHERE r.lesson_id = l.id)))
		 ORDER BY id`, seconds(RoomClosesAfter))
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	finished := 0
	var errs []error
	for _, id := range ids {
		closed, err := s.closeLesson(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("урок %d: %w", id, err))
			continue
		}
		if closed {
			finished++
		}
	}
	return finished, errors.Join(errs...)
}

func (s *Store) closeLesson(ctx context.Context, lessonID int64) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	closed, err := closeLessonIfOver(ctx, tx, lessonID)
	if err != nil {
		return false, err
	}
	return closed, tx.Commit(ctx)
}

// JobRecordingReady — точка вставки ASR (S1-5, ADR-013): запись принята,
// урок в processing. С ключом AssemblyAI worker заводит транскрипт
// и ставит asr_submit (S2-1, ADR-015); без ключа остаётся плейсхолдер —
// урок сразу done.
const JobRecordingReady = "recording_ready"

// RecordingReady — payload джобы JobRecordingReady.
type RecordingReady struct {
	LessonID int64 `json:"lesson_id"`
}

// FinishLessonProcessing — обработка записи завершена, урок done. Повторный
// вызов (at-least-once доставка джобы) ничего не меняет.
func (s *Store) FinishLessonProcessing(ctx context.Context, lessonID int64) error {
	return finishLesson(ctx, s.pool, lessonID)
}

// finishLesson — переход processing → done: в пуле или в транзакции
// вызывающего (итог транскрипта, ADR-016).
func finishLesson(ctx context.Context, db jobs.DB, lessonID int64) error {
	_, err := db.Exec(ctx,
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
