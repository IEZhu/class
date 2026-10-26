package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/livekit/protocol/livekit"

	"github.com/IEZhu/class/backend/internal/store"
)

// Доска урока (S3-6, ADR-020). Редактор Excalidraw живёт в web, сообщения
// между участниками пересылает ретранслятор excalidraw-room (сервис
// whiteboard в compose), а api выдаёт участникам ключ комнаты, хранит
// сцену и решает за caddy, кого пускать к ретранслятору.

const (
	// whiteboardSnapshotDelay — пока на доске рисуют, снимок в хранилище
	// не чаще раза в минуту; конец урока снимает сразу (room_finished).
	whiteboardSnapshotDelay = time.Minute
	// maxSceneBytes — потолок тела сохранения сцены. Столько же по
	// умолчанию пропускают host-nginx (client_max_body_size) и
	// ретранслятор (maxHttpBufferSize socket.io): больше всё равно не дойдёт.
	maxSceneBytes = 1 << 20
	// Путь ретранслятора снаружи — /whiteboard/lesson-{id}/socket.io/
	// (Caddyfile): по нему caddy и api узнают урок
	whiteboardPathPrefix = "/whiteboard/"
	whiteboardSocketPath = "socket.io"
)

type whiteboardResponse struct {
	Room string `json:"room"`
	// Key — ключ AES-GCM комнаты (base64url, 128 бит): ретранслятор
	// пересылает всем в комнате, поэтому сообщения шифруются им
	Key string `json:"key"`
	// SocketPath — путь socket.io, по которому caddy узнаёт урок
	SocketPath string `json:"socket_path"`
}

type saveSceneRequest struct {
	Elements []json.RawMessage `json:"elements"`
}

type saveSceneResponse struct {
	Version int64 `json:"version"`
}

// whiteboardOpen — рисовать можно, пока урок не ушёл в обработку записи:
// та же граница, что у комнаты (handleRoomToken).
func whiteboardOpen(d *store.LessonDetail) bool {
	return d.Status != "processing" && d.Status != "done"
}

// whiteboardLesson — урок из пути, если пользователь его видит; иначе
// ответ уже записан и ok=false.
func (a *API) whiteboardLesson(w http.ResponseWriter, r *http.Request) (*store.LessonDetail, bool) {
	lessonID, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	return a.visibleLesson(r.Context(), w, userFrom(r.Context()), lessonID)
}

func (a *API) visibleLesson(ctx context.Context, w http.ResponseWriter, u *store.User, lessonID int64) (*store.LessonDetail, bool) {
	d, err := a.store.GetLessonDetail(ctx, lessonID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "урок не найден")
		return nil, false
	}
	if err != nil {
		internalError(w, "get lesson", err)
		return nil, false
	}
	// Доска — часть урока: видят и рисуют только его участники
	if !canSeeLesson(u, d) {
		writeError(w, http.StatusForbidden, "forbidden", "нет доступа к этому уроку")
		return nil, false
	}
	return d, true
}

func whiteboardNotConfigured(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "whiteboard_not_configured", "доска не включена на стенде")
}

func lessonFinished(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "lesson_finished", "урок уже завершён")
}

// handleWhiteboard — вход на доску: комната, ключ и путь ретранслятора.
// Доска заводится при первом открытии.
func (a *API) handleWhiteboard(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.WhiteboardEnabled {
		whiteboardNotConfigured(w)
		return
	}
	d, ok := a.whiteboardLesson(w, r)
	if !ok {
		return
	}
	if !whiteboardOpen(d) {
		lessonFinished(w)
		return
	}
	newKey, err := newRoomKey()
	if err != nil {
		internalError(w, "room key", err)
		return
	}
	key, err := a.store.OpenWhiteboard(r.Context(), d.ID, newKey)
	if err != nil {
		internalError(w, "open whiteboard", err)
		return
	}
	room := roomName(d.ID)
	writeJSON(w, http.StatusOK, whiteboardResponse{
		Room: room, Key: key, SocketPath: whiteboardPathPrefix + room + "/" + whiteboardSocketPath,
	})
}

// handleWhiteboardScene — сцена в формате .excalidraw, только чтение.
// Работает и после урока, и без ретранслятора: сцена лежит в БД.
func (a *API) handleWhiteboardScene(w http.ResponseWriter, r *http.Request) {
	d, ok := a.whiteboardLesson(w, r)
	if !ok {
		return
	}
	elements, err := a.store.WhiteboardScene(r.Context(), d.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no_whiteboard", "на этом уроке доску не открывали")
		return
	}
	if err != nil {
		internalError(w, "whiteboard scene", err)
		return
	}
	body, err := store.WhiteboardFile(elements)
	if err != nil {
		internalError(w, "whiteboard file", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		logf("whiteboard scene: write: %v", err)
	}
}

// handleSaveWhiteboardScene — участник сохраняет свою сцену; api сливает
// её с сохранённой поэлементно, поэтому одновременные сохранения разных
// участников друг друга не затирают.
func (a *API) handleSaveWhiteboardScene(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.WhiteboardEnabled {
		whiteboardNotConfigured(w)
		return
	}
	d, ok := a.whiteboardLesson(w, r)
	if !ok {
		return
	}
	if !whiteboardOpen(d) {
		lessonFinished(w)
		return
	}
	var req saveSceneRequest
	if err := decodeBodyLimit(w, r, &req, maxSceneBytes); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "scene_too_large", "сцена доски больше 1 MiB")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "нужен JSON {elements: [...]}")
		return
	}
	version, err := a.store.SaveWhiteboardScene(r.Context(), d.ID, req.Elements, whiteboardSnapshotDelay)
	switch {
	case errors.Is(err, store.ErrBadScene):
		writeError(w, http.StatusBadRequest, "bad_scene", "элементы сцены не похожи на Excalidraw")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no_whiteboard", "доска этого урока ещё не открыта")
		return
	case err != nil:
		internalError(w, "save whiteboard", err)
		return
	}
	writeJSON(w, http.StatusOK, saveSceneResponse{Version: version})
}

// handleWhiteboardAuth — решение для forward_auth caddy перед каждым
// запросом к ретранслятору (Caddyfile). Урок берётся из исходного пути
// /whiteboard/lesson-{id}/socket.io/, который caddy передаёт в
// X-Forwarded-Uri, пользователь — из cookie сессии: пускаем участника
// урока, пока урок не завершён. 204 — пускать; иначе caddy отдаёт этот
// ответ клиенту. Ретранслятор комнату внутри соединения не проверяет —
// чужую комнату закрывает шифрование ключом, который выдаётся только
// участникам (ADR-020).
func (a *API) handleWhiteboardAuth(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.WhiteboardEnabled {
		whiteboardNotConfigured(w)
		return
	}
	lessonID, ok := lessonFromWhiteboardURI(r.Header.Get("X-Forwarded-Uri"))
	if !ok {
		writeError(w, http.StatusForbidden, "forbidden", "это не адрес доски урока")
		return
	}
	d, ok := a.visibleLesson(r.Context(), w, userFrom(r.Context()), lessonID)
	if !ok {
		return
	}
	if !whiteboardOpen(d) {
		lessonFinished(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// lessonFromWhiteboardURI — урок из /whiteboard/lesson-{id}/socket.io/?…
// Путь сверяется целиком: любой другой (с «..», лишними сегментами)
// не пропускается, чтобы решение относилось ровно к одному уроку.
func lessonFromWhiteboardURI(uri string) (int64, bool) {
	u, err := url.ParseRequestURI(uri)
	if err != nil {
		return 0, false
	}
	rest, found := strings.CutPrefix(u.Path, whiteboardPathPrefix)
	if !found {
		return 0, false
	}
	room, tail, found := strings.Cut(rest, "/")
	// socket.io-клиент ходит на путь со слешем на конце
	if !found || tail != whiteboardSocketPath+"/" {
		return 0, false
	}
	return lessonIDFromRoom(room)
}

// onRoomFinished — комната урока закрылась: снимаем доску сразу, не дожидаясь
// интервала. Ошибка — 503, LiveKit повторит доставку.
func (a *API) onRoomFinished(ctx context.Context, event *livekit.WebhookEvent) error {
	if event.Room == nil {
		return nil
	}
	lessonID, ok := lessonIDFromRoom(event.Room.Name)
	if !ok {
		return nil
	}
	if err := a.store.ScheduleWhiteboardSnapshot(ctx, lessonID); err != nil {
		logf("room_finished: урок %d: снимок доски не поставлен: %v", lessonID, err)
		return err
	}
	return nil
}

// newRoomKey — 128-битный ключ AES-GCM в виде JWK "k" (base64url без
// паддинга, 22 символа): так его импортирует WebCrypto в браузере.
func newRoomKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
