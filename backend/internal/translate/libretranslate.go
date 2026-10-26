// Package translate — клиент LibreTranslate (S3-3,
// docs/architecture/integrations/translation.md): перевод слова при
// кэш-миссе клика. Сервис живёт во внутренней сети compose и наружу
// не публикуется (ADR-018), ключ API ему не нужен.
package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Timeout — потолок одного перевода. Слово переводится за десятки
// миллисекунд (DoD S3-3 — меньше секунды); дольше — сервис перегружен
// или лежит, и попап лучше показать без перевода, чем ждать.
const Timeout = 3 * time.Second

// maxResponseBytes — ответ на одно слово занимает сотню байт; больше
// читать незачем.
const maxResponseBytes = 64 << 10

// LibreTranslate — клиент POST /translate.
type LibreTranslate struct {
	url  string
	http *http.Client
}

// NewLibreTranslate возвращает nil, если адрес не задан: перевод — опция
// стенда, без него клик отвечает дефинициями и translation = null.
func NewLibreTranslate(baseURL string) *LibreTranslate {
	if baseURL == "" {
		return nil
	}
	return &LibreTranslate{
		url:  strings.TrimRight(baseURL, "/") + "/translate",
		http: &http.Client{Timeout: Timeout},
	}
}

type translateRequest struct {
	Q      string `json:"q"`
	Source string `json:"source"`
	Target string `json:"target"`
	Format string `json:"format"`
}

type translateResponse struct {
	TranslatedText string `json:"translatedText"`
	Error          string `json:"error"`
}

// Translate переводит text с языка source на target (коды ISO 639-1).
func (c *LibreTranslate) Translate(ctx context.Context, text, source, target string) (string, error) {
	body, err := json.Marshal(translateRequest{Q: text, Source: source, Target: target, Format: "text"})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("libretranslate: %w", err)
	}
	defer resp.Body.Close()
	var out translateResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return "", fmt.Errorf("libretranslate: HTTP %d, ответ не JSON: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("libretranslate: HTTP %d: %s", resp.StatusCode, out.Error)
	}
	translated := strings.TrimSpace(out.TranslatedText)
	if translated == "" {
		return "", errors.New("libretranslate: пустой перевод")
	}
	return translated, nil
}
