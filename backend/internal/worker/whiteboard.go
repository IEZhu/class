package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

// SceneStorage — куда кладутся снимки досок: хранилище стенда
// (*storage.Client), в тестах — подделка.
type SceneStorage interface {
	PutObject(ctx context.Context, key, contentType string, body []byte) error
}

// whiteboardSnapshot — снимок сцены доски в whiteboards/{lesson_id}.json
// (S3-6, ADR-020). Идемпотентен: снимок уже унёс текущую версию сцены —
// ничего не делает; повтор после записи объекта, но до отметки в БД,
// перезапишет тот же объект. Урок отменили, пока объект выгружался, —
// отметка ставит его в удаление (material_files_delete), и джоба
// закончена.
func whiteboardSnapshot(st *store.Store, objects SceneStorage) jobs.Handler {
	return func(ctx context.Context, job jobs.Job) error {
		var p store.WhiteboardSnapshotJob
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("payload: %w", err)
		}
		snap, err := st.WhiteboardForSnapshot(ctx, p.LessonID)
		if errors.Is(err, store.ErrNotFound) {
			return nil // урок удалили вместе с доской — снимать нечего
		}
		if err != nil {
			return err
		}
		if snap.S3Key != "" && snap.SnapshotVersion >= snap.SceneVersion {
			return nil
		}
		body, err := store.WhiteboardFile(snap.Elements)
		if err != nil {
			return err
		}
		key := storage.WhiteboardKey(p.LessonID)
		if err := objects.PutObject(ctx, key, "application/json", body); err != nil {
			return err
		}
		err = st.MarkWhiteboardSnapshot(ctx, p.LessonID, key, snap.SceneVersion)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
}
