package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/livekit/protocol/auth"

	"github.com/IEZhu/class/backend/internal/store"
)

const (
	// roomTokenTTL — урок 60 минут плюс запас на опоздания и переподключения.
	// Токен одноразово выдаётся на вход; истёк — страница берёт новый. Дольше
	// окна комнаты он не живёт (handleRoomToken).
	roomTokenTTL = 3 * time.Hour
	// roomNamePrefix — комната на урок: lesson-{id} (integrations/livekit.md).
	roomNamePrefix = "lesson-"
)

type roomTokenResponse struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	Room  string `json:"room"`
}

// handleRoomToken — access token в комнату урока. Медиа идёт браузер ↔
// LiveKit Cloud, минуя VPS: api только подписывает право на вход.
func (a *API) handleRoomToken(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.liveKitConfigured() {
		// Стенд без ключей остаётся рабочим, комната просто недоступна
		writeError(w, http.StatusServiceUnavailable, "livekit_not_configured",
			"видеокомната не настроена")
		return
	}
	lessonID, ok := pathID(w, r)
	if !ok {
		return
	}

	u := userFrom(r.Context())
	d, err := a.store.GetLessonDetail(r.Context(), lessonID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "урок не найден")
		return
	}
	if err != nil {
		internalError(w, "get lesson", err)
		return
	}
	if !canSeeLesson(u, d) {
		writeError(w, http.StatusForbidden, "forbidden", "нет доступа к этому уроку")
		return
	}
	// В комнату пускаем только в окне урока (ADR-031) и пока урок не ушёл
	// в обработку записи. Раньше вход открыл бы комнату и запись урока,
	// которого ещё нет; позже заходить некуда, а лишний участник продлил бы
	// сессию. Токен живёт не дольше окна.
	now := time.Now()
	opens, closes := store.RoomWindow(d.StartsAt, d.EndsAt)
	switch {
	case d.Status == "processing" || d.Status == "done" || now.After(closes):
		writeError(w, http.StatusConflict, "lesson_finished", "урок уже завершён")
		return
	case now.Before(opens):
		writeError(w, http.StatusConflict, "room_not_open",
			fmt.Sprintf("комната откроется за %d минут до начала урока", int(store.RoomOpensBefore.Minutes())))
		return
	}

	token, err := roomToken(a.cfg.LiveKitAPIKey, a.cfg.LiveKitAPISecret, roomName(lessonID), u,
		min(roomTokenTTL, closes.Sub(now)))
	if err != nil {
		internalError(w, "room token", err)
		return
	}
	writeJSON(w, http.StatusOK, roomTokenResponse{
		URL: a.cfg.LiveKitURL, Token: token, Room: roomName(lessonID),
	})
}

func roomName(lessonID int64) string {
	return roomNamePrefix + strconv.FormatInt(lessonID, 10)
}

// roomToken — JWT LiveKit: identity = user_id (по нему участник сопоставляется
// с учёткой при маппинге спикеров в S2-5), имя — для подписи в комнате.
func roomToken(apiKey, apiSecret, room string, u *store.User, ttl time.Duration) (string, error) {
	canPublish := true
	canSubscribe := true
	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         room,
		CanPublish:   &canPublish,
		CanSubscribe: &canSubscribe,
	}
	return auth.NewAccessToken(apiKey, apiSecret).
		SetIdentity(strconv.FormatInt(u.ID, 10)).
		SetName(u.Name).
		SetVideoGrant(grant).
		SetValidFor(ttl).
		ToJWT()
}
