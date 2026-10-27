package livekitapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

// RemoveParticipant выставляет участника из комнаты. Пустой
// revoke_token_ts сервер заменяет на «сейчас плюс минута» (комментарий
// поля в livekit/protocol): токены участника, выданные раньше, в эту
// комнату больше не пускают. Участника в комнате нет — не ошибка:
// выставлять некого.
func (c *Client) RemoveParticipant(ctx context.Context, room, identity string) error {
	status, err := c.call(ctx, "livekit.RoomService/RemoveParticipant",
		&auth.VideoGrant{RoomAdmin: true, Room: room},
		&livekit.RoomParticipantIdentity{Room: room, Identity: identity},
		&livekit.RemoveParticipantResponse{})
	if status == http.StatusNotFound {
		// Twirp-код not_found: комнаты или участника в ней уже нет
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove participant %s from %s: %w", identity, room, err)
	}
	return nil
}
