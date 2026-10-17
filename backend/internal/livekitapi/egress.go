// Package livekitapi — тонкий клиент серверного API LiveKit.
//
// Пишем свой вместо server-sdk-go намеренно: оттуда нужен ровно один вызов
// (запуск Egress), а тянет он полный стек pion/webrtc. Протокол у LiveKit —
// Twirp поверх HTTP с protojson-телом, то есть это один POST.
package livekitapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/encoding/protojson"
)

// adminTokenTTL — токен живёт только на время запроса.
const adminTokenTTL = time.Minute

type Client struct {
	baseURL   string
	apiKey    string
	apiSecret string
	http      *http.Client
}

// New принимает тот же URL, что уходит в браузер (wss://…): для серверного
// API он же, но по https.
func New(url, apiKey, apiSecret string) *Client {
	base := strings.TrimSuffix(url, "/")
	base = strings.Replace(base, "wss://", "https://", 1)
	base = strings.Replace(base, "ws://", "http://", 1)
	return &Client{
		baseURL:   base,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		http:      &http.Client{Timeout: 15 * time.Second},
	}
}

// StartRoomCompositeEgress запускает запись комнаты в объектное хранилище.
// Возвращает EgressInfo с egress_id — по нему потом приходит webhook.
func (c *Client) StartRoomCompositeEgress(ctx context.Context, req *livekit.RoomCompositeEgressRequest) (*livekit.EgressInfo, error) {
	token, err := c.token(&auth.VideoGrant{RoomRecord: true, Room: req.RoomName})
	if err != nil {
		return nil, err
	}
	body, err := protojson.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal egress request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/twirp/livekit.Egress/StartRoomCompositeEgress", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	res, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("start egress: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck // тело только читаем

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read egress response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		// Twirp кладёт причину в тело — пробрасываем её как есть, иначе
		// в логе останется голый код и разбираться будет не с чем.
		return nil, fmt.Errorf("start egress: %s: %s", res.Status, strings.TrimSpace(string(raw)))
	}

	info := &livekit.EgressInfo{}
	// DiscardUnknown: LiveKit добавляет поля в EgressInfo быстрее, чем мы
	// обновляем protocol, и незнакомое поле не должно ронять запись урока.
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, info); err != nil {
		return nil, fmt.Errorf("unmarshal egress info: %w", err)
	}
	return info, nil
}

func (c *Client) token(grant *auth.VideoGrant) (string, error) {
	return auth.NewAccessToken(c.apiKey, c.apiSecret).
		SetVideoGrant(grant).
		SetValidFor(adminTokenTTL).
		ToJWT()
}
