package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"

	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

const (
	eventRoomStarted = "room_started"
	eventEgressEnded = "egress_ended"
)

// handleLiveKitWebhook — единственная точка, куда LiveKit сообщает о комнате
// и записи. Подпись обязательна (04-api.md): без неё кто угодно мог бы
// подменить путь к записи урока.
//
// Обработчик только меняет состояние; тяжёлой работы здесь нет — она уедет
// в worker с появлением очереди (S1-2).
func (a *API) handleLiveKitWebhook(w http.ResponseWriter, r *http.Request) {
	if a.cfg.LiveKitAPIKey == "" || a.cfg.LiveKitAPISecret == "" {
		writeError(w, http.StatusServiceUnavailable, "livekit_not_configured", "видеокомната не настроена")
		return
	}
	provider := auth.NewSimpleKeyProvider(a.cfg.LiveKitAPIKey, a.cfg.LiveKitAPISecret)
	event, err := webhook.ReceiveWebhookEvent(r, provider)
	if err != nil {
		logf("livekit webhook: подпись не принята: %v", err)
		writeError(w, http.StatusUnauthorized, "bad_signature", "подпись не принята")
		return
	}

	// LiveKit ждёт 200 и повторяет доставку при ошибке. Наши обработчики
	// идемпотентны, поэтому повтор безопасен.
	switch event.Event {
	case eventRoomStarted:
		a.onRoomStarted(r.Context(), event)
	case eventEgressEnded:
		a.onEgressEnded(r.Context(), event)
	}
	w.WriteHeader(http.StatusOK)
}

// onRoomStarted — запускаем запись урока. Ошибки только логируем: сорванная
// запись не повод отвечать LiveKit'у ошибкой и получать бесконечные ретраи.
func (a *API) onRoomStarted(ctx context.Context, event *livekit.WebhookEvent) {
	if event.Room == nil {
		return
	}
	lessonID, ok := lessonIDFromRoom(event.Room.Name)
	if !ok {
		return // чужая комната — не наша забота
	}
	if a.storage == nil {
		logf("room_started: урок %d, но хранилище не настроено — запись не ведётся", lessonID)
		return
	}
	switch err := a.store.StartLessonRecording(ctx, lessonID); {
	case errors.Is(err, store.ErrNotFound):
		// Урок уже live: повторная доставка room_started либо ручной старт
		return
	case err != nil:
		logf("room_started: урок %d: %v", lessonID, err)
		return
	}

	info, err := a.livekit.StartRoomCompositeEgress(ctx, a.egressRequest(lessonID, event.Room.Name))
	if err != nil {
		logf("room_started: запуск egress для урока %d: %v", lessonID, err)
		return
	}
	logf("room_started: урок %d, egress %s", lessonID, info.EgressId)
}

// onEgressEnded — запись дописана: сохраняем путь и двигаем статус.
func (a *API) onEgressEnded(ctx context.Context, event *livekit.WebhookEvent) {
	info := event.EgressInfo
	if info == nil {
		return
	}
	lessonID, ok := lessonIDFromRoom(info.RoomName)
	if !ok {
		return
	}
	if info.Status == livekit.EgressStatus_EGRESS_FAILED {
		logf("egress_ended: урок %d, запись не удалась: %s", lessonID, info.Error)
		return
	}
	if len(info.FileResults) == 0 {
		logf("egress_ended: урок %d, файлов в результате нет", lessonID)
		return
	}

	// Filename — путь внутри бакета, ровно тот, что мы задали в Filepath.
	key := info.FileResults[0].Filename
	// Пока ASR нет, урок сразу done; processing появится в S2-1 вместе
	// с джобой asr_submit (точка вставки — здесь).
	if err := a.store.FinishLessonRecording(ctx, lessonID, key, "done"); err != nil {
		logf("egress_ended: урок %d: %v", lessonID, err)
		return
	}
	logf("egress_ended: урок %d, запись %s (%d байт)", lessonID, key, info.FileResults[0].Size)
}

// egressRequest — что и куда писать. Учётные данные хранилища уходят
// в LiveKit в теле запроса, поэтому токен ограничен одним бакетом (ADR-009).
func (a *API) egressRequest(lessonID int64, roomName string) *livekit.RoomCompositeEgressRequest {
	fileType := livekit.EncodedFileType_MP4
	ext := "mp4"
	if a.cfg.EgressAudioOnly {
		fileType = livekit.EncodedFileType_OGG
		ext = "ogg"
	}
	return &livekit.RoomCompositeEgressRequest{
		RoomName:  roomName,
		AudioOnly: a.cfg.EgressAudioOnly,
		FileOutputs: []*livekit.EncodedFileOutput{{
			FileType: fileType,
			Filepath: storage.RecordingKey(lessonID, ext),
			// Манифест рядом с записью нам не нужен: всё состояние — в БД
			DisableManifest: true,
			Output: &livekit.EncodedFileOutput_S3{S3: &livekit.S3Upload{
				AccessKey:      a.cfg.Storage.AccessKey,
				Secret:         a.cfg.Storage.SecretKey,
				Bucket:         a.cfg.Storage.Bucket,
				Region:         a.cfg.Storage.Region,
				Endpoint:       a.cfg.Storage.Endpoint,
				ForcePathStyle: a.cfg.Storage.Endpoint != "",
			}},
		}},
	}
}

// lessonIDFromRoom — обратная функция к roomName(): lesson-42 → 42.
func lessonIDFromRoom(room string) (int64, bool) {
	rest, found := strings.CutPrefix(room, roomNamePrefix)
	if !found {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
