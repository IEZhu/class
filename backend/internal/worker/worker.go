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
func Register(r *jobs.Runner, st *store.Store, asr ASR) {
	r.Handle(store.JobRecordingReady, recordingReady(st, asr))

	// S2-1…S2-3: транскрибация AssemblyAI (ADR-015, ADR-016). Хендлеры
	// подключены и без ключа: джоба, поставленная до того, как ключ убрали,
	// доведёт урок до done, а не будет ждать вечно.
	r.Handle(store.JobASRSubmit, asrSubmit(st, asr))
	r.Handle(store.JobASRResult, asrResult(st, asr))
}

// recordingReady — точка вставки ASR (S1-5, ADR-013). С AssemblyAI урок
// получает транскрипт и джобу asr_submit (S2-1); без ключа остаётся
// плейсхолдер — урок с принятой записью сразу done.
func recordingReady(st *store.Store, asr ASR) jobs.Handler {
	return func(ctx context.Context, job jobs.Job) error {
		var p store.RecordingReady
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("payload: %w", err)
		}
		if asr.Client == nil {
			return st.FinishLessonProcessing(ctx, p.LessonID)
		}
		return st.QueueTranscript(ctx, p.LessonID, store.ProviderAssemblyAI)
	}
}
