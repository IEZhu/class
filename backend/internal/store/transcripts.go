package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
)

// Транскрипт урока (S2-1…S2-3, ADR-015): recording_ready → QueueTranscript
// (джоба asr_submit) → MarkTranscriptSubmitted (отложенная asr_poll) →
// вебхук провайдера или сверка asr_poll: TranscriptWebhook (asr_result) →
// SaveUtterances (vtt_render) → FinishTranscriptVTT. Каждый шаг сдвигает
// статус условно, поэтому повтор джобы или доставки ничего не дублирует;
// тупик — FailTranscript (ADR-016).

// Джобы транскрибации; payload у всех — TranscriptJob.
const (
	JobASRSubmit = "asr_submit"
	JobASRPoll   = "asr_poll"
	JobASRResult = "asr_result"
	JobVTTRender = "vtt_render"
)

// ASRPollAfter — через столько после отправки asr_poll сам спрашивает
// провайдера об итоге, если вебхук так и не пришёл (ADR-016). Обычно
// вебхук успевает раньше и снимает сверку.
const ASRPollAfter = 2 * time.Hour

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

// Utterance — реплика спикера; время — миллисекунды от начала записи.
type Utterance struct {
	Idx          int
	SpeakerLabel string
	// UserID — ручной маппинг спикера на участника (S2-5), до него nil
	UserID  *int64
	StartMS int64
	EndMS   int64
	Text    string
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
// по нему придёт вебхук, а на случай его потери через ASRPollAfter
// встанет сверка asr_poll — одной транзакцией. Повтор ничего не меняет.
func (s *Store) MarkTranscriptSubmitted(ctx context.Context, id int64, externalID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE transcripts SET status = 'submitted', external_id = $2
		 WHERE id = $1 AND status = 'queued'`, id, externalID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if err := jobs.EnqueueOnce(ctx, tx, JobASRPoll, TranscriptJob{TranscriptID: id}, ASRPollAfter); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// TranscriptWebhook — провайдер сообщил итог (вебхуком или в ответ
// на сверку asr_poll): транскрипт уходит в fetching вместе с джобой
// asr_result, которая заберёт результат, а ждущая сверка снимается.
// Повторная доставка (транскрипт уже не submitted) ничего не меняет
// и джобу не ставит. ErrNotFound — такого транскрипта у нас нет.
func (s *Store) TranscriptWebhook(ctx context.Context, provider, externalID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	err = tx.QueryRow(ctx,
		`UPDATE transcripts SET status = 'fetching'
		 WHERE provider = $1 AND external_id = $2 AND status = 'submitted'
		 RETURNING id`, provider, externalID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var known bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM transcripts WHERE provider = $1 AND external_id = $2)`,
			provider, externalID).Scan(&known); err != nil {
			return err
		}
		if !known {
			return ErrNotFound
		}
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := jobs.Enqueue(ctx, tx, JobASRResult, TranscriptJob{TranscriptID: id}); err != nil {
		return err
	}
	if err := jobs.Cancel(ctx, tx, JobASRPoll, TranscriptJob{TranscriptID: id}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SaveUtterances сохраняет реплики, переводит транскрипт в completed
// и ставит vtt_render — одной транзакцией. Условие на fetching делает
// вызов идемпотентным: повтор asr_result реплики не задвоит, а первичный
// ключ (transcript_id, idx) держит то же на уровне схемы. idx — порядок
// реплик в utts.
func (s *Store) SaveUtterances(ctx context.Context, id int64, utts []Utterance) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE transcripts SET status = 'completed' WHERE id = $1 AND status = 'fetching'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	speakers := make([]string, len(utts))
	starts := make([]int64, len(utts))
	ends := make([]int64, len(utts))
	texts := make([]string, len(utts))
	for i, u := range utts {
		speakers[i], starts[i], ends[i], texts[i] = u.SpeakerLabel, u.StartMS, u.EndMS, u.Text
	}
	// Один запрос на весь транскрипт: часовой урок — сотни реплик
	if _, err := tx.Exec(ctx,
		`INSERT INTO utterances (transcript_id, idx, speaker_label, start_ms, end_ms, text)
		 SELECT $1, u.ord - 1, u.speaker, u.start_ms, u.end_ms, u.text
		 FROM unnest($2::text[], $3::bigint[], $4::bigint[], $5::text[])
		      WITH ORDINALITY AS u (speaker, start_ms, end_ms, text, ord)`,
		id, speakers, starts, ends, texts); err != nil {
		return err
	}
	if _, err := jobs.Enqueue(ctx, tx, JobVTTRender, TranscriptJob{TranscriptID: id}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Utterances — реплики транскрипта по порядку.
func (s *Store) Utterances(ctx context.Context, transcriptID int64) ([]Utterance, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT idx, speaker_label, user_id, start_ms, end_ms, text
		 FROM utterances WHERE transcript_id = $1 ORDER BY idx`, transcriptID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Utterance, error) {
		var u Utterance
		err := row.Scan(&u.Idx, &u.SpeakerLabel, &u.UserID, &u.StartMS, &u.EndMS, &u.Text)
		return u, err
	})
}

// FinishTranscriptVTT — VTT выложен: путь в транскрипт, урок done. Повтор
// (перерендер после правки реплик) только обновляет путь; прошлый сбой
// выкладки забывается.
func (s *Store) FinishTranscriptVTT(ctx context.Context, id int64, vttKey string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lessonID int64
	err = tx.QueryRow(ctx,
		`UPDATE transcripts SET vtt_s3_key = $2, error = NULL
		 WHERE id = $1 AND status = 'completed' RETURNING lesson_id`, id, vttKey).Scan(&lessonID)
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

// LessonTranscript — транскрипт урока с репликами для вьюера.
// ErrNotFound — транскрипта нет (ASR не настроен или записи не было).
func (s *Store) LessonTranscript(ctx context.Context, lessonID int64) (*Transcript, []Utterance, error) {
	t := &Transcript{LessonID: lessonID}
	err := s.pool.QueryRow(ctx,
		`SELECT id, provider, status, COALESCE(external_id, ''), COALESCE(vtt_s3_key, '')
		 FROM transcripts WHERE lesson_id = $1`, lessonID).
		Scan(&t.ID, &t.Provider, &t.Status, &t.ExternalID, &t.VTTKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	utts, err := s.Utterances(ctx, t.ID)
	if err != nil {
		return nil, nil, err
	}
	return t, utts, nil
}
