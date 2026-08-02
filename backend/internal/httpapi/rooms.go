package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/livekit/protocol/auth"

	"github.com/IEZhu/class/backend/internal/store"
)

const (
	// roomTokenTTL — урок 60 минут плюс запас на опоздания и переподключения.
	// Токен одноразово выдаётся на вход; истёк — страница берёт новый.
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
	if a.cfg.LiveKitAPIKey == "" || a.cfg.LiveKitAPISecret == "" || a.cfg.LiveKitURL == "" {
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
	// В комнату пускаем до тех пор, пока урок не ушёл в обработку записи:
	// после этого заходить некуда, а лишний участник продлил бы сессию.
	if d.Status == "processing" || d.Status == "done" {
		writeError(w, http.StatusConflict, "lesson_finished", "урок уже завершён")
		return
	}

	token, err := roomToken(a.cfg.LiveKitAPIKey, a.cfg.LiveKitAPISecret, roomName(lessonID), u)
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
func roomToken(apiKey, apiSecret, room string, u *store.User) (string, error) {
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
		SetValidFor(roomTokenTTL).
		ToJWT()
}
