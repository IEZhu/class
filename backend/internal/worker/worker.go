// Package worker — хендлеры фоновых джоб по kind. Очередь, ретраи и захват —
// в internal/jobs; здесь только то, что делать с каждой джобой. Доставка
// at-least-once, поэтому каждый хендлер идемпотентен.
package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/storage"
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

// MaterialFiles — то, что worker'у нужно от хранилища (storage.Client).
type MaterialFiles interface {
	Delete(ctx context.Context, key string) error
}

// RegisterMaterialFiles — долг D-4 (ADR-024): джоба material_files_delete
// убирает объекты файлов материалов, строк которых уже нет. Зовётся,
// только когда хранилище настроено; без него kind не регистрируется,
// и джобы ждут в очереди, пока хранилище не появится.
func RegisterMaterialFiles(r *jobs.Runner, objects MaterialFiles) {
	r.Handle(store.JobMaterialFilesDelete, materialFilesDelete(objects))
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
