package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/IEZhu/class/backend/internal/assemblyai"
	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

// ASR — зависимости транскрибации (ADR-015). Нулевое значение — ключа
// AssemblyAI нет: урок с записью сразу уходит в done.
type ASR struct {
	Client *assemblyai.Client
	// Storage — presigned GET записи для AssemblyAI и выгрузка VTT
	Storage *storage.Client
	// WebhookURL — внешний адрес POST /webhooks/assemblyai
	WebhookURL string
}

const (
	// asrAudioTTL — срок ссылки на запись для AssemblyAI. Файл он скачивает
	// в начале обработки, обычно через минуты; сутки — запас на его очередь
	// (ADR-015). Ссылка уходит только AssemblyAI, не в браузер.
	asrAudioTTL = 24 * time.Hour
	// failTimeout — на запись итога «транскрибация сдалась»: контекст
	// попытки к этому моменту может быть уже исчерпан.
	failTimeout = 5 * time.Second
)

var errASRNotConfigured = errors.New("AssemblyAI не настроен: ASSEMBLYAI_API_KEY пуст")

// asrSubmit — S2-1: presigned GET записи → AssemblyAI. Отправленный
// транскрипт повтор джобы не трогает.
func asrSubmit(st *store.Store, asr ASR) jobs.Handler {
	return transcriptStep(st, func(ctx context.Context, t *store.Transcript) error {
		if t.Status != store.TranscriptQueued {
			return nil
		}
		if asr.Client == nil {
			return st.FailTranscript(ctx, t.ID, errASRNotConfigured.Error())
		}
		audioURL, err := asr.Storage.PresignGet(ctx, t.RecordingKey, asrAudioTTL)
		if err != nil {
			return err
		}
		externalID, err := asr.Client.Submit(ctx, audioURL, asr.WebhookURL)
		if err != nil {
			return err
		}
		log.Printf("worker: урок %d отправлен в AssemblyAI, транскрипт %s", t.LessonID, externalID)
		return st.MarkTranscriptSubmitted(ctx, t.ID, externalID)
	})
}

// asrResult — S2-2: вебхук сообщил итог, забираем реплики. Ошибка у
// AssemblyAI — итог, а не повод повторять: та же запись упадёт так же
// (ADR-016).
func asrResult(st *store.Store, asr ASR) jobs.Handler {
	return transcriptStep(st, func(ctx context.Context, t *store.Transcript) error {
		if t.Status != store.TranscriptFetching {
			return nil
		}
		if asr.Client == nil {
			return st.FailTranscript(ctx, t.ID, errASRNotConfigured.Error())
		}
		res, err := asr.Client.Transcript(ctx, t.ExternalID)
		if err != nil {
			return err
		}
		switch res.Status {
		case assemblyai.StatusCompleted:
			return st.SaveUtterances(ctx, t.ID, utterancesFrom(res.Utterances))
		case assemblyai.StatusError:
			log.Printf("worker: AssemblyAI не распознал урок %d: %s", t.LessonID, res.Error)
			return st.FailTranscript(ctx, t.ID, "AssemblyAI: "+clean(res.Error))
		}
		// Вебхук приходит только по итогу: промежуточный статус — повторим позже
		return fmt.Errorf("AssemblyAI после вебхука вернул статус %q", res.Status)
	})
}

// transcriptStep — общая часть джоб транскрибации: payload, строка
// транскрипта и исход ошибки. Ошибка шага — повтор через очередь с её
// backoff, а на последней попытке транскрибация сдаётся (ADR-016):
// транскрипт error, урок done. Иначе джоба умерла бы, а урок навсегда
// остался бы в processing.
func transcriptStep(st *store.Store, step func(context.Context, *store.Transcript) error) jobs.Handler {
	return func(ctx context.Context, job jobs.Job) error {
		var p store.TranscriptJob
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("payload: %w", err)
		}
		t, err := st.TranscriptByID(ctx, p.TranscriptID)
		if errors.Is(err, store.ErrNotFound) {
			return nil // урок удалён вместе с транскриптом
		}
		if err != nil {
			return err
		}
		err = step(ctx, t)
		if err == nil || !job.LastAttempt {
			return err
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failTimeout)
		defer cancel()
		if ferr := st.FailTranscript(fctx, t.ID, clean(err.Error())); ferr != nil {
			return errors.Join(err, ferr)
		}
		log.Printf("worker: %s: транскрипт %d урока %d сдался после %d попыток: %v",
			job.Kind, t.ID, t.LessonID, job.Attempt, err)
		return nil
	}
}

// utterancesFrom — реплики провайдера в наши. Данные внешние, а схема
// требует 0 ≤ start ≤ end: одна кривая реплика не должна ронять весь
// транскрипт.
func utterancesFrom(in []assemblyai.Utterance) []store.Utterance {
	out := make([]store.Utterance, len(in))
	for i, u := range in {
		start := max(u.Start, 0)
		out[i] = store.Utterance{
			SpeakerLabel: clean(u.Speaker),
			StartMS:      start,
			EndMS:        max(u.End, start),
			Text:         clean(u.Text),
		}
	}
	return out
}

// clean — NUL Postgres в text не принимает, а JSON его пропускает (\u0000).
func clean(s string) string { return strings.ReplaceAll(s, "\x00", "") }
