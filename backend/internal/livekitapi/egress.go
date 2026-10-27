// Package livekitapi — тонкий клиент серверного API LiveKit.
//
// Пишем свой вместо server-sdk-go намеренно: оттуда нужны три вызова
// (запуск и остановка Egress, RemoveParticipant при архивации), а тянет
// он полный стек pion/webrtc. Протокол у LiveKit — Twirp поверх HTTP
// с protojson-телом, то есть это один POST.
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
	"google.golang.org/protobuf/proto"
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
	// Меняем только схему в начале: «ws://» в середине адреса не трогаем
	if rest, ok := strings.CutPrefix(base, "wss://"); ok {
		base = "https://" + rest
	} else if rest, ok := strings.CutPrefix(base, "ws://"); ok {
		base = "http://" + rest
	}
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
	info := &livekit.EgressInfo{}
	if _, err := c.call(ctx, "livekit.Egress/StartRoomCompositeEgress",
		&auth.VideoGrant{RoomRecord: true, Room: req.RoomName}, req, info); err != nil {
		return nil, fmt.Errorf("start egress: %w", err)
	}
	return info, nil
}

// StopEgress останавливает запись: Egress дописывает файл, выгружает его
// и присылает egress_ended. stopped — остановили этим вызовом. Записи уже
// нет или она уже кончается — не ошибка, а stopped = false: LiveKit
// отвечает not_found или failed_precondition («egress with status …
// cannot be stopped»), итог придёт или пришёл вебхуком.
func (c *Client) StopEgress(ctx context.Context, egressID string) (stopped bool, err error) {
	status, err := c.call(ctx, "livekit.Egress/StopEgress",
		&auth.VideoGrant{RoomRecord: true}, &livekit.StopEgressRequest{EgressId: egressID}, &livekit.EgressInfo{})
	if status == http.StatusNotFound || status == http.StatusPreconditionFailed {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stop egress %s: %w", egressID, err)
	}
	return true, nil
}

// call — один вызов Twirp: POST protojson-тела на /twirp/<метод> с токеном
// на время запроса. Возвращает и HTTP-статус ответа, чтобы вызывающий
// отличил «нет такого» от сбоя.
func (c *Client) call(ctx context.Context, method string, grant *auth.VideoGrant, req, resp proto.Message) (int, error) {
	token, err := c.token(grant)
	if err != nil {
		return 0, err
	}
	body, err := protojson.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/twirp/"+method, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	res, err := c.http.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close() //nolint:errcheck // тело только читаем

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		// Twirp кладёт причину в тело — пробрасываем её как есть, иначе
		// в логе останется голый код и разбираться будет не с чем.
		return res.StatusCode, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(raw)))
	}
	// DiscardUnknown: LiveKit добавляет поля в ответы быстрее, чем мы
	// обновляем protocol, и незнакомое поле не должно ронять вызов.
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, resp); err != nil {
		return res.StatusCode, fmt.Errorf("unmarshal response: %w", err)
	}
	return res.StatusCode, nil
}

func (c *Client) token(grant *auth.VideoGrant) (string, error) {
	return auth.NewAccessToken(c.apiKey, c.apiSecret).
		SetVideoGrant(grant).
		SetValidFor(adminTokenTTL).
		ToJWT()
}
