package llm

import (
	"context"
	"net/http"
	"strings"
)

// OpenAI Responses API (developers.openai.com/api/docs/guides/text):
// POST /v1/responses, ключ в Authorization: Bearer.
const openAIBaseURL = "https://api.openai.com"

type openAI struct{ *client }

type openAIRequest struct {
	Model        string `json:"model"`
	Instructions string `json:"instructions,omitempty"`
	Input        string `json:"input"`
	// MaxOutputTokens — потолок вывода вместе с токенами рассуждения
	MaxOutputTokens int             `json:"max_output_tokens"`
	Reasoning       openAIReasoning `json:"reasoning"`
	// Store false — ответ не хранится у OpenAI: продолжать диалог
	// нам не нужно, а текст и так лежит в generated_texts
	Store bool `json:"store"`
}

type openAIReasoning struct {
	Effort string `json:"effort"`
}

type openAIResponse struct {
	Model string `json:"model"`
	// Status — completed; incomplete с причиной в incomplete_details
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	// Output — элементы ответа: message с output_text, reasoning и др.;
	// текст — сумма всех output_text
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func (p *openAI) Generate(ctx context.Context, req Request) (Response, error) {
	body := openAIRequest{
		Model: p.model, Instructions: req.System, Input: req.Prompt,
		MaxOutputTokens: req.MaxTokens, Reasoning: openAIReasoning{Effort: "low"},
	}
	var out openAIResponse
	auth := func(h http.Header) { h.Set("Authorization", "Bearer "+p.apiKey) }
	if err := p.post(ctx, p.url(openAIBaseURL, "/v1/responses"), auth, body, &out); err != nil {
		return Response{}, err
	}
	if out.Status != "completed" {
		reason := ""
		if out.IncompleteDetails != nil {
			reason = out.IncompleteDetails.Reason
		}
		if reason == "max_output_tokens" {
			return Response{}, p.fail(http.StatusOK, ErrTruncated, "")
		}
		return Response{}, p.fail(http.StatusOK, ErrRejected, "status "+out.Status+" "+reason)
	}
	var text strings.Builder
	for _, item := range out.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
	}
	return p.finish(text.String(), out.Model)
}
