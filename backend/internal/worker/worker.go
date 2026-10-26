// Package worker — хендлеры фоновых джоб по kind. Очередь, ретраи и захват —
// в internal/jobs; здесь только то, что делать с каждой джобой. Доставка
// at-least-once, поэтому каждый хендлер идемпотентен.
package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/store"
)

// Register подключает к runner все джобы worker'а.
func Register(r *jobs.Runner, st *store.Store) {
	r.Handle(store.JobRecordingReady, recordingReady(st))
}

// recordingReady — плейсхолдер точки вставки ASR (S1-5, ADR-013): пока
// транскрибации нет, урок с принятой записью сразу done. S2-1 заменит его
// джобой asr_submit.
func recordingReady(st *store.Store) jobs.Handler {
	return func(ctx context.Context, job jobs.Job) error {
		var p store.RecordingReady
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("payload: %w", err)
		}
		return st.FinishLessonProcessing(ctx, p.LessonID)
	}
}
