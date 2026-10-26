// Package assemblyai — тонкий клиент REST API AssemblyAI v2 (ADR-015):
// отправка записи на транскрибацию с диаризацией и забор результата
// по репликам. SDK у AssemblyAI для Go нет, а нужны ровно два вызова.
//
// Аудио через VPS не идёт: AssemblyAI сам скачивает запись по presigned
// GET из хранилища. О готовности он сообщает вебхуком с секретным
// заголовком (WebhookHeader), который api сверяет с тем же секретом.
package assemblyai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL — американский регион API (EU — api.eu.assemblyai.com).
const DefaultBaseURL = "https://api.assemblyai.com"

// WebhookHeader — заголовок, в котором AssemblyAI возвращает секрет
// в каждом вебхуке (webhook_auth_header_name при отправке).
const WebhookHeader = "X-Lingua-Webhook-Secret"

// MinWebhookSecretLen — короче секрет подбирается перебором по открытому
// эндпоинту вебхука. В шаблоне .env — openssl rand -hex 32.
const MinWebhookSecretLen = 32

// Итоговые статусы транскрипта у AssemblyAI — только с ними приходит
// вебхук; queued и processing — промежуточные.
const (
	StatusCompleted = "completed"
	StatusError     = "error"
)

// speechModels — модель задаём явно: по документации умолчание зависит
// от тарифа аккаунта. universal-2 — $0.15/ч, как в бюджете (ADR-015).
var speechModels = []string{"universal-2"}

var ErrNoWebhookSecret = errors.New("assemblyai: ASSEMBLYAI_API_KEY задан, а ASSEMBLYAI_WEBHOOK_SECRET пуст")

// CheckWebhookSecret — секрет вебхука годится: задан и не короче
// MinWebhookSecretLen. Общая проверка для api и worker.
func CheckWebhookSecret(secret string) error {
	if len(secret) < MinWebhookSecretLen {
		return fmt.Errorf("assemblyai: ASSEMBLYAI_WEBHOOK_SECRET короче %d символов", MinWebhookSecretLen)
	}
	return nil
}

type Config struct {
	APIKey        string
	WebhookSecret string
	// BaseURL пуст — DefaultBaseURL; в тестах — адрес fake-сервера
	BaseURL string
}

type Client struct {
	baseURL       string
	apiKey        string
	webhookSecret string
	http          *http.Client
}

// New возвращает nil без ошибки, если ключа нет: транскрибация выключена,
// урок с записью сразу уходит в done (ADR-013). Ключ без годного секрета
// вебхука — ошибка конфигурации: результат было бы нечем принять.
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, nil
	}
	if cfg.WebhookSecret == "" {
		return nil, ErrNoWebhookSecret
	}
	if err := CheckWebhookSecret(cfg.WebhookSecret); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		baseURL:       base,
		apiKey:        cfg.APIKey,
		webhookSecret: cfg.WebhookSecret,
		http:          &http.Client{Timeout: 30 * time.Second},
	}, nil
}

type submitRequest struct {
	AudioURL     string   `json:"audio_url"`
	SpeechModels []string `json:"speech_models"`
	LanguageCode string   `json:"language_code"`
	// speaker_labels по документации требует punctuate — ставим явно
	Punctuate     bool   `json:"punctuate"`
	SpeakerLabels bool   `json:"speaker_labels"`
	WebhookURL    string `json:"webhook_url"`
	WebhookHeader string `json:"webhook_auth_header_name"`
	WebhookSecret string `json:"webhook_auth_header_value"`
}

// Transcript — ответ GET /v2/transcript/{id}; лишние поля отбрасываются.
type Transcript struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// Error — причина при status=error
	Error      string      `json:"error"`
	Utterances []Utterance `json:"utterances"`
}

// Utterance — реплика одного спикера; время — миллисекунды от начала записи.
type Utterance struct {
	Speaker string `json:"speaker"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Text    string `json:"text"`
}

// Submit ставит запись в очередь AssemblyAI: английский, метки спикеров,
// вебхук на webhookURL с секретом в WebhookHeader. Возвращает id
// транскрипта — по нему придёт вебхук. speakers_expected не задаём:
// снапшот участников урока не говорит, сколько человек реально говорило,
// а неверное число портит диаризацию сильнее автоопределения (ADR-015).
func (c *Client) Submit(ctx context.Context, audioURL, webhookURL string) (string, error) {
	body, err := json.Marshal(submitRequest{
		AudioURL:      audioURL,
		SpeechModels:  speechModels,
		LanguageCode:  "en",
		Punctuate:     true,
		SpeakerLabels: true,
		WebhookURL:    webhookURL,
		WebhookHeader: WebhookHeader,
		WebhookSecret: c.webhookSecret,
	})
	if err != nil {
		return "", fmt.Errorf("assemblyai: marshal submit: %w", err)
	}
	var t Transcript
	if err := c.do(ctx, http.MethodPost, "/v2/transcript", body, &t); err != nil {
		return "", fmt.Errorf("assemblyai: submit: %w", err)
	}
	if t.ID == "" {
		return "", errors.New("assemblyai: submit: в ответе нет id транскрипта")
	}
	return t.ID, nil
}

// Transcript забирает транскрипт с репликами по id.
func (c *Client) Transcript(ctx context.Context, id string) (*Transcript, error) {
	t := &Transcript{}
	if err := c.do(ctx, http.MethodGet, "/v2/transcript/"+url.PathEscape(id), nil, t); err != nil {
		return nil, fmt.Errorf("assemblyai: transcript %s: %w", id, err)
	}
	return t, nil
}

const (
	// maxResponseBytes — транскрипт часового урока со словами и таймкодами
	// весит единицы мегабайт; потолок с большим запасом.
	maxResponseBytes = 64 << 20
	// maxErrorBytes — сколько тела ошибки попадает в лог и last_error
	maxErrorBytes = 1 << 10
)

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close() //nolint:errcheck // тело только читаем

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		// Причина у AssemblyAI — {"error": "..."}; пробрасываем начало тела
		// как есть, иначе в логе останется голый код. Обрезка может
		// разрезать символ, а Postgres не примет такой текст в last_error.
		head := strings.ToValidUTF8(string(raw[:min(len(raw), maxErrorBytes)]), "")
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(head))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
