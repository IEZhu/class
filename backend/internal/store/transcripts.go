package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
)

// Транскрипт урока (S2-1…S2-3, ADR-015): recording_ready → QueueTranscript
// (джоба asr_submit) → MarkTranscriptSubmitted → вебхук провайдера
// TranscriptWebhook (asr_result) → SaveUtterances (vtt_render) →
// FinishTranscriptVTT. Каждый шаг сдвигает статус условно, поэтому
// повтор джобы или доставки ничего не дублирует; тупик — FailTranscript
// (ADR-016).

// JobASRSubmit — отправка записи провайдеру; payload — TranscriptJob.
const JobASRSubmit = "asr_submit"

// TranscriptJob — payload джоб транскрибации.
type TranscriptJob struct {
	TranscriptID int64 `json:"transcript_id"`
}

// ProviderAssemblyAI — единственный провайдер ASR (CHECK в 0008).
const ProviderAssemblyAI = "assemblyai"

// Статусы transcripts.status, смысл — в миграции 0008.
const (
	TranscriptQueued    = "queued"
	TranscriptSubmitted = "submitted"
	TranscriptFetching  = "fetching"
	TranscriptCompleted = "completed"
	TranscriptError     = "error"
)

type Transcript struct {
	ID         int64
	LessonID   int64
	Provider   string
	Status     string
	ExternalID string
	VTTKey     string
	// RecordingKey — запись урока, которую получает провайдер
	RecordingKey string
}

// QueueTranscript заводит транскрипт урока и ставит asr_submit одной
// транзакцией. Транскрипт у урока один, поэтому повтор recording_ready
// (at-least-once) не заведёт второй и не поставит вторую джобу.
func (s *Store) QueueTranscript(ctx context.Context, lessonID int64, provider string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	err = tx.QueryRow(ctx,
		`INSERT INTO transcripts (lesson_id, provider) VALUES ($1, $2)
		 ON CONFLICT (lesson_id) DO NOTHING RETURNING id`, lessonID, provider).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE lessons SET transcript_id = $2 WHERE id = $1`, lessonID, id); err != nil {
		return err
	}
	if _, err := jobs.Enqueue(ctx, tx, JobASRSubmit, TranscriptJob{TranscriptID: id}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// TranscriptByID — транскрипт с путём записи урока.
func (s *Store) TranscriptByID(ctx context.Context, id int64) (*Transcript, error) {
	t := &Transcript{}
	err := s.pool.QueryRow(ctx,
		`SELECT t.id, t.lesson_id, t.provider, t.status, COALESCE(t.external_id, ''),
		        COALESCE(t.vtt_s3_key, ''), COALESCE(l.recording_s3_key, '')
		 FROM transcripts t JOIN lessons l ON l.id = t.lesson_id
		 WHERE t.id = $1`, id).
		Scan(&t.ID, &t.LessonID, &t.Provider, &t.Status, &t.ExternalID, &t.VTTKey, &t.RecordingKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// MarkTranscriptSubmitted — провайдер принял запись и выдал свой id:
// по нему придёт вебхук. Повтор ничего не меняет.
func (s *Store) MarkTranscriptSubmitted(ctx context.Context, id int64, externalID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE transcripts SET status = 'submitted', external_id = $2
		 WHERE id = $1 AND status = 'queued'`, id, externalID)
	return err
}

// FailTranscript — транскрибация сдалась (ADR-016): причина в error, урок
// done — запись смотрится и без транскрипта. Сохранённые реплики остаются:
// completed не понижается, несостоявшийся VTT виден по error.
func (s *Store) FailTranscript(ctx context.Context, id int64, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lessonID int64
	err = tx.QueryRow(ctx,
		`UPDATE transcripts SET error = $2,
		        status = CASE WHEN status = 'completed' THEN status ELSE 'error' END
		 WHERE id = $1 RETURNING lesson_id`, id, reason).Scan(&lessonID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := finishLesson(ctx, tx, lessonID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
