package llm

import (
	"context"
	"net/http"
	"strings"
)

// Anthropic Messages API (platform.claude.com/docs/en/api/messages):
// POST /v1/messages, ключ в x-api-key, версия API в anthropic-version.
const (
	anthropicBaseURL = "https://api.anthropic.com"
	anthropicVersion = "2023-06-01"
)

type anthropic struct{ *client }

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	// effort low: у текущих моделей рассуждение включено по умолчанию
	// и тратит max_tokens; для короткого учебного текста хватает
	// минимального (ADR-029). temperature не шлём: текущие модели
	// отвечают на неё 400.
	OutputConfig anthropicOutputConfig `json:"output_config"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort"`
}

type anthropicResponse struct {
	Model string `json:"model"`
	// Content — блоки ответа: text и (пустые при display omitted) thinking
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
}

func (p *anthropic) Generate(ctx context.Context, req Request) (Response, error) {
	body := anthropicRequest{
		Model: p.model, MaxTokens: req.MaxTokens, System: req.System,
		Messages:     []anthropicMessage{{Role: "user", Content: req.Prompt}},
		OutputConfig: anthropicOutputConfig{Effort: "low"},
	}
	var out anthropicResponse
	auth := func(h http.Header) {
		h.Set("x-api-key", p.apiKey)
		h.Set("anthropic-version", anthropicVersion)
	}
	if err := p.post(ctx, p.url(anthropicBaseURL, "/v1/messages"), auth, body, &out); err != nil {
		return Response{}, err
	}
	switch out.StopReason {
	case "max_tokens", "model_context_window_exceeded":
		return Response{}, p.fail(http.StatusOK, ErrTruncated, "stop_reason "+out.StopReason)
	case "refusal":
		return Response{}, p.fail(http.StatusOK, ErrRejected, "stop_reason refusal")
	}
	var text strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return p.finish(text.String(), out.Model)
}
