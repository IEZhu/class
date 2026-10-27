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

	// maxWebhookBody — потолок тела вебхука LiveKit (ADR-031). Подпись
	// проверяется только по прочитанному телу, поэтому без потолка любой
	// заставил бы api буферизовать гигабайты; события LiveKit — килобайты.
	maxWebhookBody = 1 << 20
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
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBody)
	provider := auth.NewSimpleKeyProvider(a.cfg.LiveKitAPIKey, a.cfg.LiveKitAPISecret)
	event, err := webhook.ReceiveWebhookEvent(r, provider)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "тело вебхука больше 1 MiB")
		return
	}
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
			// Сегмент не заведён — повтор доставки запустит запись заново
			writeError(w, http.StatusServiceUnavailable, "recording_not_started", "запись не запустилась")
			return
		}
	case eventEgressEnded:
		if err := a.onEgressEnded(r.Context(), event); err != nil {
			// Итог сегмента не сохранён — повтор доставки сохранит его заново
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

// startRecording — запускаем сегмент записи урока, если в этой сессии
// комнаты он ещё не пишется (ADR-012, ADR-031). Кроме room_started, сюда
// ведёт и participant_joined: если запуск сорвался или room_started не дошёл,
// следующий вошедший участник запустит запись. Вне окна комнаты урок
// не трогаем. Ошибка возвращается, только когда запуск не удался и его
// стоит повторить.
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
	req, err := a.egressRequest(lessonID, event.Room.Name)
	if err != nil {
		return err
	}
	key := req.FileOutputs[0].Filepath
	var egressID string
	err = a.store.StartLessonRecording(ctx, lessonID, event.Room.Sid, key, func(ctx context.Context) (string, error) {
		info, err := a.livekit.StartRoomCompositeEgress(ctx, req)
		if err != nil {
			return "", fmt.Errorf("запуск egress: %w", err)
		}
		egressID = info.EgressId
		return egressID, nil
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Запись в этой сессии уже идёт (повторная доставка, следующий
		// участник), её прямо сейчас запускает параллельная доставка, окно
		// комнаты закрыто либо урок завершён
		return nil
	case err != nil:
		logf("%s: урок %d: %v", event.Event, lessonID, err)
		return err
	}
	logf("%s: урок %d, egress %s → %s", event.Event, lessonID, egressID, key)
	return nil
}

// onEgressEnded — Egress закончил сегмент: файл принят или сегмент
// не удался. Урок при этом идёт дальше, пока открыто окно комнаты:
// опустевшая посреди урока комната — конец сегмента, а не урока (ADR-031).
// Ошибка возвращается, только когда итог не сохранился и повтор доставки
// его сохранит; чужую комнату и уже принятый сегмент повторять незачем.
func (a *API) onEgressEnded(ctx context.Context, event *livekit.WebhookEvent) error {
	info := event.EgressInfo
	if info == nil {
		return nil
	}
	lessonID, ok := lessonIDFromRoom(info.RoomName)
	if !ok {
		return nil
	}
	if info.EgressId == "" {
		logf("egress_ended: урок %d без id Egress — сегмент не найти", lessonID)
		return nil
	}
	res := store.RecordingResult{EgressID: info.EgressId, RoomSID: info.RoomId}
	if len(info.FileResults) > 0 {
		// Filename — путь внутри бакета, ровно тот, что мы задали в Filepath
		res.Key = info.FileResults[0].Filename
		res.Size = info.FileResults[0].Size
	}
	switch {
	case info.Status == livekit.EgressStatus_EGRESS_FAILED || info.Status == livekit.EgressStatus_EGRESS_ABORTED:
		// Файл, если он и есть, оборван: сегмент failed, ключ — для разбора
		res.Error = info.Status.String()
		if info.Error != "" {
			res.Error += ": " + info.Error
		}
	case res.Key == "":
		res.Error = "файлов в результате нет"
	}

	switch err := a.store.EndLessonRecording(ctx, lessonID, res); {
	case errors.Is(err, store.ErrNotFound):
		// Урока нет либо сегмент уже принят (повторная доставка)
		return nil
	case err != nil:
		// Транзакция откатилась целиком; повтор доставки сделает то же заново
		logf("egress_ended: урок %d, egress %s: %v", lessonID, info.EgressId, err)
		return err
	}
	if res.Error != "" {
		logf("egress_ended: урок %d, egress %s не записал сегмент: %s", lessonID, info.EgressId, res.Error)
		return nil
	}
	logf("egress_ended: урок %d, сегмент %s (%d байт)", lessonID, res.Key, res.Size)
	return nil
}

// egressRequest — что и куда писать: у каждого запуска свой файл
// (storage.NewRecordingKey). Учётные данные хранилища уходят в LiveKit
// в теле запроса, поэтому токен ограничен одним бакетом (ADR-009).
func (a *API) egressRequest(lessonID int64, roomName string) (*livekit.RoomCompositeEgressRequest, error) {
	fileType := livekit.EncodedFileType_MP4
	ext := "mp4"
	if a.cfg.EgressAudioOnly {
		fileType = livekit.EncodedFileType_OGG
		ext = "ogg"
	}
	key, err := storage.NewRecordingKey(lessonID, ext)
	if err != nil {
		return nil, err
	}
	return &livekit.RoomCompositeEgressRequest{
		RoomName:  roomName,
		AudioOnly: a.cfg.EgressAudioOnly,
		FileOutputs: []*livekit.EncodedFileOutput{{
			FileType: fileType,
			Filepath: key,
			// Манифест рядом с записью нам не нужен: всё состояние — в БД
			DisableManifest: true,
			Output: &livekit.EncodedFileOutput_S3{S3: &livekit.S3Upload{
				AccessKey: a.cfg.Storage.AccessKey,
				Secret:    a.cfg.Storage.SecretKey,
				Bucket:    a.cfg.Storage.Bucket,
				// Регион, уже разрешённый storage.New (у R2 — auto)
				Region:         a.cfg.Storage.Region,
				Endpoint:       a.cfg.Storage.Endpoint,
				ForcePathStyle: a.cfg.Storage.Endpoint != "",
			}},
		}},
	}, nil
}

// lessonIDFromRoom — обратная функция к roomName(): lesson-42 → 42. Только
// каноническая запись: ParseInt принял бы и lesson-+42, и lesson-042, и
// чужая комната с таким именем попала бы в запись урока 42.
func lessonIDFromRoom(room string) (int64, bool) {
	rest, found := strings.CutPrefix(room, roomNamePrefix)
	if !found {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 || roomName(id) != room {
		return 0, false
	}
	return id, true
}
