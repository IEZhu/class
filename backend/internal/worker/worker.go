// Package worker — хендлеры фоновых джоб по kind. Очередь, ретраи и захват —
// в internal/jobs; здесь только то, что делать с каждой джобой. Доставка
// at-least-once, поэтому каждый хендлер идемпотентен.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/IEZhu/class/backend/internal/gcal"
	"github.com/IEZhu/class/backend/internal/harvester"
	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

// Register подключает к runner все джобы worker'а.
func Register(r *jobs.Runner, st *store.Store, asr ASR) {
	r.Handle(store.JobRecordingReady, recordingReady(st, asr))

	// S2-1…S2-3: транскрибация AssemblyAI (ADR-015, ADR-016). Хендлеры
	// подключены и без ключа: джоба, поставленная до того, как ключ убрали,
	// доведёт урок до done, а не будет ждать вечно.
	r.Handle(store.JobASRSubmit, asrSubmit(st, asr))
	r.Handle(store.JobASRPoll, asrPoll(st, asr))
	r.Handle(store.JobASRResult, asrResult(st, asr))
	r.Handle(store.JobVTTRender, vttRender(st, asr))
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

// RegisterCalendar — S1-1: джоба calendar_sync (ADR-023). cal == nil —
// Google не настроен: создание и перенос снимаются со строкой в журнале,
// удаления событий ждут в очереди (calendarSync.remove).
// publicBaseURL — https://<DOMAIN> для ссылки на урок в описании события.
func RegisterCalendar(r *jobs.Runner, st *store.Store, cal *gcal.Client, publicBaseURL string) {
	r.Handle(store.JobCalendarSync, calendarSync{st: st, cal: cal, publicBaseURL: publicBaseURL}.handle)
}

// RegisterWhiteboard — S3-6: снимки досок в хранилище (ADR-020). Зовётся,
// только когда хранилище настроено; без него kind не регистрируется,
// и джобы ждут в очереди — по одной на доску (jobs.EnqueueOnce), — пока
// хранилище не появится.
func RegisterWhiteboard(r *jobs.Runner, st *store.Store, objects SceneStorage) {
	r.Handle(store.JobWhiteboardSnapshot, whiteboardSnapshot(st, objects))
}

// RegisterMaterialFiles — долг D-4 (ADR-024): джоба material_files_delete
// убирает объекты файлов материалов, строк которых уже нет. Зовётся,
// только когда хранилище настроено; без него kind не регистрируется,
// и джобы ждут в очереди, пока хранилище не появится.
func RegisterMaterialFiles(r *jobs.Runner, objects MaterialFiles) {
	r.Handle(store.JobMaterialFilesDelete, materialFilesDelete(objects))
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

// MaterialFiles — то, что worker'у нужно от хранилища (storage.Client).
type MaterialFiles interface {
	Delete(ctx context.Context, key string) error
}

// materialFilesDelete удаляет постоянный объект и загрузку под
// materials-pending/ каждого ключа. Отсутствующий ключ — не ошибка, поэтому
// повтор после сбоя хранилища доделывает то, что не успела прошлая попытка.
func materialFilesDelete(objects MaterialFiles) jobs.Handler {
	return func(ctx context.Context, job jobs.Job) error {
		var p store.MaterialFilesDelete
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("payload: %w", err)
		}
		for _, key := range p.Keys {
			for _, k := range storage.MaterialFileKeys(key) {
				if err := objects.Delete(ctx, k); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

// recordingSweepEvery — период уборки записей: урок уходит дальше
// не позже чем через 5 минут после закрытия окна комнаты.
const recordingSweepEvery = 5 * time.Minute

// RegisterRecordingSweep — F1 (ADR-031): уборка записей. Урок, чьё окно
// комнаты закрылось, а egress_ended его не завершил (комната опустела
// до конца урока, запись упала, вебхук потерялся), уходит в processing
// с выбранной записью или в done без неё. Джоба периодическая, расписание
// ставится рядом с хендлером (ADR-021).
func RegisterRecordingSweep(r *jobs.Runner, s *jobs.Scheduler, st *store.Store) {
	r.Handle(store.JobRecordingSweep, func(ctx context.Context, _ jobs.Job) error {
		n, err := st.SweepRecordings(ctx)
		if n > 0 {
			log.Printf("worker: уборка записей завершила уроков: %d", n)
		}
		return err
	})
	s.Every(store.JobRecordingSweep, recordingSweepEvery)
}
