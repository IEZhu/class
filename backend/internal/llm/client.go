package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
)

const (
	// maxResponseBytes — ответ с текстом в сотни слов весит килобайты;
	// больше — не наш ответ, читать его незачем.
	maxResponseBytes = 1 << 20
	// maxTextBytes — потолок самого текста: 400 слов — около 3 KB.
	maxTextBytes = 64 << 10
	// maxDetailBytes — сколько причины от провайдера попадает в ошибку и лог
	maxDetailBytes = 300
)

// client — общее у трёх провайдеров: адрес, ключ, POST JSON с потолками
// и разбор ошибок.
type client struct {
	name    string
	model   string
	apiKey  string
	baseURL string
	http    *http.Client
}

func newHTTPClient() *http.Client { return &http.Client{Timeout: Timeout} }

func (c *client) Name() string  { return c.name }
func (c *client) Model() string { return c.model }

func (c *client) url(defaultBase, path string) string {
	base := c.baseURL
	if base == "" {
		base = defaultBase
	}
	return base + path
}

func (c *client) fail(status int, kind error, detail string) *Error {
	return &Error{Provider: c.name, Status: status, Kind: kind, Detail: c.clean(detail)}
}

// clean — причину от провайдера в лог: без ключа, валидный UTF-8
// (обрезка не режет символ), не длиннее maxDetailBytes.
func (c *client) clean(s string) string {
	if c.apiKey != "" {
		s = strings.ReplaceAll(s, c.apiKey, "***")
	}
	s = strings.TrimSpace(s)
	if len(s) > maxDetailBytes {
		s = s[:maxDetailBytes]
	}
	return strings.ToValidUTF8(s, "")
}

// post шлёт body JSON-ом на url и разбирает ответ 200 в out. auth
// выставляет заголовки ключа своего провайдера.
func (c *client) post(ctx context.Context, url string, auth func(http.Header), body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return c.fail(0, ErrBadResponse, "marshal: "+err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return c.fail(0, ErrRejected, err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	auth(req.Header)
	res, err := c.http.Do(req)
	if err != nil {
		return c.fail(0, transportKind(err), err.Error())
	}
	defer res.Body.Close() //nolint:errcheck // тело только читаем

	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return c.fail(res.StatusCode, transportKind(err), "read: "+err.Error())
	}
	if len(data) > maxResponseBytes {
		return c.fail(res.StatusCode, ErrTooLarge, "")
	}
	if res.StatusCode != http.StatusOK {
		kind := statusKind(res.StatusCode)
		detail := ""
		// Причина отказа авторизации не нужна, а OpenAI повторяет в ней
		// замаскированный ключ
		if kind != ErrAuth {
			detail = errorDetail(data)
		}
		return c.fail(res.StatusCode, kind, detail)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return c.fail(res.StatusCode, ErrBadResponse, err.Error())
	}
	return nil
}

// statusKind — HTTP-статус ответа провайдера в вид сбоя. 529 —
// перегрузка у Anthropic, 504 — его таймаут обработки.
func statusKind(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusPaymentRequired || status == http.StatusForbidden:
		return ErrAuth
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return ErrTimeout
	case status >= 500:
		return ErrUnavailable
	default:
		return ErrRejected
	}
}

func transportKind(err error) error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return ErrTimeout
	}
	return ErrUnavailable
}

// errorDetail — сообщение из тела ошибки. У всех трёх провайдеров оно
// в error.message (Anthropic {"type":"error","error":{"type","message"}},
// OpenAI и совместимый с ним DeepSeek {"error":{"message","type","code"}});
// другое тело — его начало как есть.
func errorDetail(data []byte) string {
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil && body.Error.Message != "" {
		if body.Error.Type != "" {
			return body.Error.Type + ": " + body.Error.Message
		}
		return body.Error.Message
	}
	return string(data)
}

// finish — общая проверка текста ответа.
func (c *client) finish(text, model string) (Response, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Response{}, c.fail(http.StatusOK, ErrEmpty, "")
	}
	if len(text) > maxTextBytes {
		return Response{}, c.fail(http.StatusOK, ErrTooLarge, "")
	}
	if model == "" {
		model = c.model
	}
	return Response{Text: text, Model: model}, nil
}
