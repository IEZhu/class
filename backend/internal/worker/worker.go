// Package worker — хендлеры фоновых джоб по kind. Очередь, ретраи и захват —
// в internal/jobs; здесь только то, что делать с каждой джобой. Доставка
// at-least-once, поэтому каждый хендлер идемпотентен.
package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/IEZhu/class/backend/internal/harvester"
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

// S5-1/S5-2: сборщик субтитров — отдельная роль worker'а (WORKER_ROLE=
// harvester, ADR-022), потому что ему нужен yt-dlp и свой IP. Обе джобы
// периодические (ADR-021): расписание ставит тот процесс, что их выполняет.
func RegisterHarvester(r *jobs.Runner, s *jobs.Scheduler, h *harvester.Harvester) {
	r.Handle(harvester.JobRSSPoll, func(ctx context.Context, _ jobs.Job) error { return h.PollFeeds(ctx) })
	r.Handle(harvester.JobSubtitlesFetch, func(ctx context.Context, _ jobs.Job) error { return h.FetchSubtitles(ctx) })
	s.Every(harvester.JobRSSPoll, harvester.PollEvery)
	s.Every(harvester.JobSubtitlesFetch, harvester.FetchEvery)
}
