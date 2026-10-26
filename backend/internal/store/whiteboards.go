package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
)

// Доска урока (S3-6, ADR-020). Ретранслятор Excalidraw только пересылает
// зашифрованные сообщения между участниками, поэтому сцена, которую они
// сохраняют, живёт в whiteboards, а её снимок в хранилище делает worker.

// JobWhiteboardSnapshot — снять сцену доски в хранилище
// (whiteboards/{lesson_id}.json). Ставится сохранением сцены не чаще раза
// в интервал и концом урока (room_finished) — сразу.
const JobWhiteboardSnapshot = "whiteboard_snapshot"

// WhiteboardSnapshotJob — payload джобы JobWhiteboardSnapshot.
type WhiteboardSnapshotJob struct {
	LessonID int64 `json:"lesson_id"`
}

// ErrBadScene — присланные элементы не похожи на элементы Excalidraw.
var ErrBadScene = errors.New("bad whiteboard scene")

// maxElementIDLen — id элементов Excalidraw — nanoid из 21 символа;
// потолок с запасом отсекает мусор, а не чужие форматы.
const maxElementIDLen = 128

// OpenWhiteboard возвращает ключ комнаты доски урока, заводя доску
// с ключом newKey при первом открытии. Параллельные первые открытия
// получают один и тот же ключ: проигравшая вставка читает строку
// победившей.
func (s *Store) OpenWhiteboard(ctx context.Context, lessonID int64, newKey string) (string, error) {
	var key string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO whiteboards (lesson_id, room_key) VALUES ($1, $2)
		 ON CONFLICT (lesson_id) DO UPDATE SET room_key = whiteboards.room_key
		 RETURNING room_key`, lessonID, newKey).Scan(&key)
	return key, err
}

// WhiteboardScene — элементы сцены доски урока как есть, вместе
// с удалёнными. ErrNotFound — доску не открывали.
func (s *Store) WhiteboardScene(ctx context.Context, lessonID int64) (json.RawMessage, error) {
	var scene json.RawMessage
	err := s.pool.QueryRow(ctx,
		`SELECT scene FROM whiteboards WHERE lesson_id = $1`, lessonID).Scan(&scene)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return scene, err
}

// SaveWhiteboardScene сливает присланные элементы с сохранёнными (правило —
// mergeElements) и ставит снимок не позже чем через snapshotDelay. Строка
// доски захвачена до конца транзакции: сохранения одной доски идут по
// очереди, и ни одно не затирает элементы другого. Если слияние ничего
// не изменило, версия и очередь не трогаются. Возвращает версию сцены;
// ErrNotFound — доску не открывали, ErrBadScene — мусор в элементах.
func (s *Store) SaveWhiteboardScene(ctx context.Context, lessonID int64, incoming []json.RawMessage, snapshotDelay time.Duration) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var raw []byte
	var version int64
	err = tx.QueryRow(ctx,
		`SELECT scene, scene_version FROM whiteboards WHERE lesson_id = $1 FOR UPDATE`,
		lessonID).Scan(&raw, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	var stored []json.RawMessage
	if err := json.Unmarshal(raw, &stored); err != nil {
		return 0, fmt.Errorf("stored scene of lesson %d: %w", lessonID, err)
	}
	merged, changed, err := mergeElements(stored, incoming)
	if err != nil {
		return 0, err
	}
	if !changed {
		return version, nil
	}
	scene, err := json.Marshal(merged)
	if err != nil {
		return 0, err
	}
	if err := tx.QueryRow(ctx,
		`UPDATE whiteboards SET scene = $2, scene_version = scene_version + 1, updated_at = now()
		 WHERE lesson_id = $1 RETURNING scene_version`, lessonID, scene).Scan(&version); err != nil {
		return 0, err
	}
	if err := jobs.EnqueueOnce(ctx, tx, JobWhiteboardSnapshot,
		WhiteboardSnapshotJob{LessonID: lessonID}, snapshotDelay); err != nil {
		return 0, err
	}
	return version, tx.Commit(ctx)
}

// ScheduleWhiteboardSnapshot — снимок сразу: урок кончился. У урока
// без доски снимать нечего, джоба не ставится.
func (s *Store) ScheduleWhiteboardSnapshot(ctx context.Context, lessonID int64) error {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM whiteboards WHERE lesson_id = $1)`, lessonID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return jobs.EnqueueOnce(ctx, s.pool, JobWhiteboardSnapshot, WhiteboardSnapshotJob{LessonID: lessonID}, 0)
}

// WhiteboardSnapshot — то, что нужно worker'у для снимка доски.
type WhiteboardSnapshot struct {
	Elements        json.RawMessage
	SceneVersion    int64
	SnapshotVersion int64
	// S3Key пуст, пока снимка не было
	S3Key string
}

// WhiteboardForSnapshot — сцена и версии доски; ErrNotFound — доски нет
// (урок удалили вместе с ней).
func (s *Store) WhiteboardForSnapshot(ctx context.Context, lessonID int64) (*WhiteboardSnapshot, error) {
	w := &WhiteboardSnapshot{}
	var key *string
	err := s.pool.QueryRow(ctx,
		`SELECT scene, scene_version, snapshot_version, scene_s3_key FROM whiteboards WHERE lesson_id = $1`,
		lessonID).Scan(&w.Elements, &w.SceneVersion, &w.SnapshotVersion, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if key != nil {
		w.S3Key = *key
	}
	return w, nil
}

// MarkWhiteboardSnapshot — в хранилище лежит сцена версии version.
// GREATEST: запоздавший повтор старой джобы не откатит отметку назад.
func (s *Store) MarkWhiteboardSnapshot(ctx context.Context, lessonID int64, key string, version int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE whiteboards SET scene_s3_key = $2, snapshot_version = GREATEST(snapshot_version, $3)
		 WHERE lesson_id = $1`, lessonID, key, version)
	return err
}

// WhiteboardFile — сцена в формате файла .excalidraw: снимок открывается
// в любом Excalidraw («Открыть»), а S3-7 берёт из elements текстовые
// элементы. Элементы — как есть, с удалёнными: их Excalidraw не рисует.
func WhiteboardFile(elements json.RawMessage) ([]byte, error) {
	return json.Marshal(struct {
		Type     string          `json:"type"`
		Version  int             `json:"version"`
		Source   string          `json:"source"`
		Elements json.RawMessage `json:"elements"`
		AppState struct{}        `json:"appState"`
		Files    struct{}        `json:"files"`
	}{Type: "excalidraw", Version: 2, Source: "lingua-class", Elements: elements})
}

// elementHeader — поля элемента Excalidraw, по которым сливаются сцены.
type elementHeader struct {
	ID           string `json:"id"`
	Version      *int64 `json:"version"`
	VersionNonce *int64 `json:"versionNonce"`
	// Index — дробный индекс z-порядка (base62, сравнивается как строка)
	Index *string `json:"index"`
}

func parseHeader(raw json.RawMessage) (elementHeader, error) {
	var h elementHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		return h, fmt.Errorf("%w: %v", ErrBadScene, err)
	}
	if h.ID == "" || len(h.ID) > maxElementIDLen || h.Version == nil || h.VersionNonce == nil {
		return h, fmt.Errorf("%w: элементу нужны id, version и versionNonce", ErrBadScene)
	}
	return h, nil
}

// mergeElements — слияние сцен по правилу Excalidraw (reconcileElements):
// из двух версий элемента остаётся бо́льшая version, при равных — меньший
// versionNonce. Сохранённые элементы, которых нет в присланных, остаются:
// участник мог их ещё не получить. Удаление — это тоже версия элемента
// (isDeleted), поэтому оно не воскресает от сохранения отставшего
// участника. Порядок — z-порядок Excalidraw: по дробному index, при равных
// по id (orderByFractionalIndex); если index есть не у всех — как
// у присланных, дальше остальные сохранённые. changed — итог отличается
// от сохранённого.
func mergeElements(stored, incoming []json.RawMessage) (merged []json.RawMessage, changed bool, err error) {
	type entry struct {
		raw json.RawMessage
		h   elementHeader
	}
	storedIDs := make([]string, 0, len(stored))
	storedByID := make(map[string]entry, len(stored))
	for _, raw := range stored {
		h, err := parseHeader(raw)
		if err != nil {
			return nil, false, err
		}
		storedIDs = append(storedIDs, h.ID)
		storedByID[h.ID] = entry{raw, h}
	}
	out := make([]entry, 0, len(stored)+len(incoming))
	seen := make(map[string]bool, len(incoming))
	for _, raw := range incoming {
		h, err := parseHeader(raw)
		if err != nil {
			return nil, false, err
		}
		if seen[h.ID] {
			return nil, false, fmt.Errorf("%w: элемент %q прислан дважды", ErrBadScene, h.ID)
		}
		seen[h.ID] = true
		old, ok := storedByID[h.ID]
		keepStored := ok && (*old.h.Version > *h.Version ||
			(*old.h.Version == *h.Version && *old.h.VersionNonce <= *h.VersionNonce))
		if keepStored {
			out = append(out, old)
			continue
		}
		out = append(out, entry{raw, h})
		changed = true
	}
	for _, id := range storedIDs {
		if !seen[id] {
			out = append(out, storedByID[id])
		}
	}
	indexed := !slices.ContainsFunc(out, func(e entry) bool { return e.h.Index == nil || *e.h.Index == "" })
	if indexed {
		slices.SortStableFunc(out, func(a, b entry) int {
			if c := strings.Compare(*a.h.Index, *b.h.Index); c != 0 {
				return c
			}
			return strings.Compare(a.h.ID, b.h.ID)
		})
	}
	merged = make([]json.RawMessage, len(out))
	for i, e := range out {
		merged[i] = e.raw
	}
	return merged, changed, nil
}
