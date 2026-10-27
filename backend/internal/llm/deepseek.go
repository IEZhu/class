package llm

import (
	"context"
	"net/http"
)

// DeepSeek Chat Completions (api-docs.deepseek.com/api/create-chat-completion),
// совместимый с OpenAI формат: POST /chat/completions, ключ в
// Authorization: Bearer.
const deepSeekBaseURL = "https://api.deepseek.com"

type deepSeek struct{ *client }

type deepSeekRequest struct {
	Model     string            `json:"model"`
	Messages  []deepSeekMessage `json:"messages"`
	MaxTokens int               `json:"max_tokens"`
	// Thinking disabled: по умолчанию у DeepSeek рассуждение включено,
	// учебному тексту оно не нужно (ADR-029)
	Thinking deepSeekThinking `json:"thinking"`
	Stream   bool             `json:"stream"`
}

type deepSeekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type deepSeekThinking struct {
	Type string `json:"type"`
}

type deepSeekResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		// FinishReason — stop | length | content_filter |
		// insufficient_system_resource | …
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func (p *deepSeek) Generate(ctx context.Context, req Request) (Response, error) {
	messages := []deepSeekMessage{{Role: "user", Content: req.Prompt}}
	if req.System != "" {
		messages = append([]deepSeekMessage{{Role: "system", Content: req.System}}, messages...)
	}
	body := deepSeekRequest{
		Model: p.model, Messages: messages, MaxTokens: req.MaxTokens,
		Thinking: deepSeekThinking{Type: "disabled"},
	}
	var out deepSeekResponse
	auth := func(h http.Header) { h.Set("Authorization", "Bearer "+p.apiKey) }
	if err := p.post(ctx, p.url(deepSeekBaseURL, "/chat/completions"), auth, body, &out); err != nil {
		return Response{}, err
	}
	if len(out.Choices) == 0 {
		return Response{}, p.fail(http.StatusOK, ErrEmpty, "нет choices")
	}
	choice := out.Choices[0]
	switch choice.FinishReason {
	case "stop":
	case "length":
		return Response{}, p.fail(http.StatusOK, ErrTruncated, "")
	case "content_filter":
		return Response{}, p.fail(http.StatusOK, ErrRejected, "finish_reason content_filter")
	default:
		// insufficient_system_resource, aborted — сбой на их стороне
		return Response{}, p.fail(http.StatusOK, ErrUnavailable, "finish_reason "+choice.FinishReason)
	}
	return p.finish(choice.Message.Content, out.Model)
}
