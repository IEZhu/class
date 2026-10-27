package httpapi

import (
	"context"
	"errors"
	"fmt"
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
	eventRoomStarted       = "room_started"
	eventParticipantJoined = "participant_joined"
	eventEgressEnded       = "egress_ended"
	// S3-6: комната закрылась — снять доску урока сразу
	eventRoomFinished = "room_finished"
)

// handleLiveKitWebhook — единственная точка, куда LiveKit сообщает о комнате
// и записи. Подпись обязательна (04-api.md): без неё кто угодно мог бы
// подменить путь к записи урока.
//
// Обработчик только меняет состояние урока и ставит джобы в jobs; тяжёлая
// работа — в worker (S1-2, ADR-013). Исключение — запуск Egress: это
// синхронный вызов LiveKit, пока строка урока занята (ADR-012).
func (a *API) handleLiveKitWebhook(w http.ResponseWriter, r *http.Request) {
	// Без URL запись не запустить: урок ушёл бы в попытку Egress по пустому
	// адресу. Граница та же, что у комнаты (handleRoomToken).
	if !a.cfg.liveKitConfigured() {
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

	// LiveKit ждёт 2xx и на 5xx повторяет доставку (ограниченно, с паузами).
	// Наши обработчики идемпотентны, поэтому повтор безопасен.
	switch event.Event {
	case eventRoomStarted, eventParticipantJoined:
		if err := a.startRecording(r.Context(), event); err != nil {
			// Урок остался scheduled — повтор доставки запустит запись заново
			writeError(w, http.StatusServiceUnavailable, "recording_not_started", "запись не запустилась")
			return
		}
	case eventEgressEnded:
		if err := a.onEgressEnded(r.Context(), event); err != nil {
			// Путь записи не сохранён — повтор доставки сохранит его заново
			writeError(w, http.StatusServiceUnavailable, "recording_not_saved", "запись не сохранена")
			return
		}
	case eventRoomFinished:
		if err := a.onRoomFinished(r.Context(), event); err != nil {
			writeError(w, http.StatusServiceUnavailable, "snapshot_not_scheduled", "снимок доски не поставлен")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// startRecording — запускаем запись урока, если она ещё не идёт (ADR-012).
// Кроме room_started, сюда ведёт и participant_joined: если запуск сорвался
// или room_started не дошёл, следующий вошедший участник запустит запись.
// Ошибка возвращается, только когда запуск не удался и его стоит повторить.
func (a *API) startRecording(ctx context.Context, event *livekit.WebhookEvent) error {
	if event.Room == nil {
		return nil
	}
	lessonID, ok := lessonIDFromRoom(event.Room.Name)
	if !ok {
		return nil // чужая комната — не наша забота
	}
	if a.storage == nil {
		logf("%s: урок %d, но хранилище не настроено — запись не ведётся", event.Event, lessonID)
		return nil
	}
	var egressID string
	err := a.store.StartLessonRecording(ctx, lessonID, func(ctx context.Context) error {
		info, err := a.livekit.StartRoomCompositeEgress(ctx, a.egressRequest(lessonID, event.Room.Name))
		if err != nil {
			return fmt.Errorf("запуск egress: %w", err)
		}
		egressID = info.EgressId
		return nil
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Запись уже идёт (повторная доставка, следующий участник), её
		// прямо сейчас запускает параллельная доставка либо урок завершён
		return nil
	case err != nil:
		logf("%s: урок %d: %v", event.Event, lessonID, err)
		return err
	}
	logf("%s: урок %d, egress %s", event.Event, lessonID, egressID)
	return nil
}

// onEgressEnded — запись дописана: сохраняем путь и двигаем статус.
// Ошибка возвращается, только когда путь не сохранился и повтор доставки
// его сохранит; чужую комнату, сбой записи, пустой результат и уже
// принятую запись повторять незачем.
func (a *API) onEgressEnded(ctx context.Context, event *livekit.WebhookEvent) error {
	info := event.EgressInfo
	if info == nil {
		return nil
	}
	lessonID, ok := lessonIDFromRoom(info.RoomName)
	if !ok {
		return nil
	}
	if info.Status == livekit.EgressStatus_EGRESS_FAILED {
		logf("egress_ended: урок %d, запись не удалась: %s", lessonID, info.Error)
		return nil
	}
	if len(info.FileResults) == 0 {
		logf("egress_ended: урок %d, файлов в результате нет", lessonID)
		return nil
	}

	// Filename — путь внутри бакета, ровно тот, что мы задали в Filepath.
	key := info.FileResults[0].Filename
	// Точка вставки ASR: урок уходит в processing вместе с джобой
	// recording_ready; worker отправляет запись в AssemblyAI (S2-1),
	// а без ключа сразу ставит done.
	switch err := a.store.FinishLessonRecording(ctx, lessonID, key); {
	case errors.Is(err, store.ErrNotFound):
		// Повторная доставка egress_ended: запись уже принята
		return nil
	case err != nil:
		// Транзакция откатилась целиком; условие на статус делает повтор
		// доставки безопасным
		logf("egress_ended: урок %d: %v", lessonID, err)
		return err
	}
	logf("egress_ended: урок %d, запись %s (%d байт)", lessonID, key, info.FileResults[0].Size)
	return nil
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
