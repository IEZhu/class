package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "fixture-llm-key-0123456789"

// testProvider — провайдер name, нацеленный на httptest-сервер с h.
func testProvider(t *testing.T, name string, h http.HandlerFunc) Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(Config{Provider: name, APIKey: testKey, BaseURL: srv.URL + "/"})
	if err != nil || p == nil {
		t.Fatalf("New(%s): %v, %v", name, p, err)
	}
	return p
}

func clientOf(p Provider) *client {
	switch p := p.(type) {
	case *anthropic:
		return p.client
	case *openAI:
		return p.client
	case *deepSeek:
		return p.client
	}
	return nil
}

// decodeBody — тело запроса JSON-ом; сломанное — 400 в ответ fake-сервера.
func decodeBody(t *testing.T, w http.ResponseWriter, r *http.Request) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil
	}
	return got
}

var testRequest = Request{System: "You write texts.", Prompt: "<target_words>\nrun\n</target_words>", MaxTokens: 1500}

// Без ключа генерация выключена и это не ошибка; неизвестный провайдер —
// ошибка и без ключа. Пустой LLM_PROVIDER — первый провайдер документа,
// пустой LLM_MODEL — модель провайдера по умолчанию.
func TestNew(t *testing.T) {
	if p, err := New(Config{}); p != nil || err != nil {
		t.Errorf("без ключа: (%v, %v), хотели (nil, nil)", p, err)
	}
	for _, cfg := range []Config{{Provider: "gemini"}, {Provider: "gemini", APIKey: testKey}} {
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "gemini") {
			t.Errorf("неизвестный провайдер %+v: %v", cfg, err)
		}
	}
	for _, c := range []struct {
		cfg         Config
		name, model string
	}{
		{Config{APIKey: testKey}, ProviderAnthropic, "claude-haiku-5-5"},
		{Config{Provider: " OpenAI ", APIKey: testKey}, ProviderOpenAI, "gpt-6-luna"},
		{Config{Provider: "deepseek", APIKey: testKey}, ProviderDeepSeek, "deepseek-flash"},
		{Config{Provider: "anthropic", APIKey: testKey, Model: "claude-sonnet-5-5"}, ProviderAnthropic, "claude-sonnet-5-5"},
	} {
		p, err := New(c.cfg)
		if err != nil || p.Name() != c.name || p.Model() != c.model {
			t.Errorf("%+v: %v %v", c.cfg, p, err)
			continue
		}
		if clientOf(p).http.Timeout != Timeout {
			t.Errorf("%s: таймаут клиента %v", c.name, clientOf(p).http.Timeout)
		}
	}
}

// Запрос по документации Messages API: POST /v1/messages, x-api-key,
// anthropic-version, system отдельно от messages, effort low; текст —
// сумма text-блоков, thinking-блоки пропускаются.
func TestAnthropicGenerate(t *testing.T) {
	var got map[string]any
	p := testProvider(t, ProviderAnthropic, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" ||
			r.Header.Get("x-api-key") != testKey || r.Header.Get("anthropic-version") != "2023-06-01" ||
			r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" {
			http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, http.StatusUnauthorized)
			return
		}
		if got = decodeBody(t, w, r); got == nil {
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-5-5",
			"content":[{"type":"thinking","thinking":"","signature":"x"},{"type":"text","text":"Tom likes to run. "},{"type":"text","text":"He runs daily."}],
			"stop_reason":"end_turn","usage":{"input_tokens":30,"output_tokens":12}}`))
	})
	resp, err := p.Generate(context.Background(), testRequest)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "Tom likes to run. He runs daily." || resp.Model != "claude-haiku-5-5" {
		t.Errorf("ответ %+v", resp)
	}
	want := map[string]any{
		"model": "claude-haiku-5-5", "max_tokens": float64(1500), "system": "You write texts.",
		"messages":      []any{map[string]any{"role": "user", "content": testRequest.Prompt}},
		"output_config": map[string]any{"effort": "low"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("тело запроса\n%v\nхотели\n%v", got, want)
	}
}

// Запрос по документации Responses API: POST /v1/responses, Bearer,
// instructions + input, max_output_tokens, effort low, store false;
// текст — output_text из элементов message, reasoning пропускается.
func TestOpenAIGenerate(t *testing.T) {
	var got map[string]any
	p := testProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer "+testKey {
			http.Error(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`, http.StatusUnauthorized)
			return
		}
		if got = decodeBody(t, w, r); got == nil {
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","model":"gpt-6-luna",
			"output":[{"type":"reasoning","summary":[]},{"type":"message","role":"assistant",
			"content":[{"type":"output_text","text":"Tom likes to run.","annotations":[]}]}]}`))
	})
	resp, err := p.Generate(context.Background(), testRequest)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "Tom likes to run." || resp.Model != "gpt-6-luna" {
		t.Errorf("ответ %+v", resp)
	}
	want := map[string]any{
		"model": "gpt-6-luna", "instructions": "You write texts.", "input": testRequest.Prompt,
		"max_output_tokens": float64(1500), "reasoning": map[string]any{"effort": "low"}, "store": false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("тело запроса\n%v\nхотели\n%v", got, want)
	}
}

// Запрос по документации DeepSeek: POST /chat/completions, Bearer,
// system + user в messages, thinking disabled, без стрима.
func TestDeepSeekGenerate(t *testing.T) {
	var got map[string]any
	p := testProvider(t, ProviderDeepSeek, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer "+testKey {
			http.Error(w, `{"error":{"message":"Authentication Fails","type":"authentication_error"}}`, http.StatusUnauthorized)
			return
		}
		if got = decodeBody(t, w, r); got == nil {
			return
		}
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"deepseek-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Tom likes to run."},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":30,"completion_tokens":6,"total_tokens":36}}`))
	})
	resp, err := p.Generate(context.Background(), testRequest)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "Tom likes to run." || resp.Model != "deepseek-flash" {
		t.Errorf("ответ %+v", resp)
	}
	want := map[string]any{
		"model": "deepseek-flash", "max_tokens": float64(1500), "stream": false,
		"thinking": map[string]any{"type": "disabled"},
		"messages": []any{
			map[string]any{"role": "system", "content": "You write texts."},
			map[string]any{"role": "user", "content": testRequest.Prompt},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("тело запроса\n%v\nхотели\n%v", got, want)
	}
}

// Статусы ответа в виды сбоя — одинаково у всех провайдеров. Ключ не
// попадает в ошибку, даже если провайдер повторяет его в теле.
func TestErrorMapping(t *testing.T) {
	for _, name := range Providers {
		for _, c := range []struct {
			status    int
			body      string
			kind      error
			temporary bool
			detail    string
		}{
			{401, `{"error":{"type":"authentication_error","message":"Incorrect API key provided: ` + testKey + `"}}`, ErrAuth, false, ""},
			{402, `{"error":{"message":"Insufficient Balance"}}`, ErrAuth, false, ""},
			{403, `{"error":{"message":"forbidden"}}`, ErrAuth, false, ""},
			{429, `{"error":{"type":"rate_limit_error","message":"slow down"}}`, ErrRateLimited, true, "rate_limit_error: slow down"},
			{529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, ErrUnavailable, true, "overloaded_error: Overloaded"},
			{500, `upstream crashed`, ErrUnavailable, true, "upstream crashed"},
			{504, `{"error":{"type":"timeout_error","message":"timed out"}}`, ErrTimeout, true, "timeout_error: timed out"},
			{400, `{"error":{"type":"invalid_request_error","message":"bad key ` + testKey + ` here"}}`, ErrRejected, false, "invalid_request_error: bad key *** here"},
			{422, strings.Repeat("я", 400), ErrRejected, false, strings.Repeat("я", maxDetailBytes/2)},
		} {
			p := testProvider(t, name, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			})
			_, err := p.Generate(context.Background(), testRequest)
			var e *Error
			if !errors.As(err, &e) || !errors.Is(err, c.kind) || e.Status != c.status || e.Provider != name {
				t.Errorf("%s HTTP %d: %v, хотели %v", name, c.status, err, c.kind)
				continue
			}
			if Temporary(err) != c.temporary {
				t.Errorf("%s HTTP %d: Temporary = %v", name, c.status, !c.temporary)
			}
			if e.Detail != c.detail {
				t.Errorf("%s HTTP %d: detail %q, хотели %q", name, c.status, e.Detail, c.detail)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("%s HTTP %d: ключ в ошибке: %v", name, c.status, err)
			}
		}
	}
}

// Переадресация провайдера не выполняется: ключ (x-api-key у Anthropic,
// Bearer у остальных) и промпт не уходят на чужой адрес, а 3xx — отказ
// провайдера ErrRejected со статусом и адресом переадресации.
func TestRedirectRefused(t *testing.T) {
	for _, name := range Providers {
		for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			var leaked atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				leaked.Add(1)
				_, _ = w.Write([]byte(`{}`))
			}))
			p := testProvider(t, name, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+r.URL.Path, status)
			})
			_, err := p.Generate(context.Background(), testRequest)
			target.Close()
			var e *Error
			if !errors.As(err, &e) || !errors.Is(err, ErrRejected) || e.Status != status || Temporary(err) {
				t.Errorf("%s HTTP %d: %v, хотели отказ ErrRejected", name, status, err)
			}
			if e != nil && !strings.Contains(e.Detail, target.URL) {
				t.Errorf("%s HTTP %d: в причине нет адреса переадресации: %q", name, status, e.Detail)
			}
			if n := leaked.Load(); n != 0 {
				t.Errorf("%s HTTP %d: запрос ушёл по переадресации %d раз", name, status, n)
			}
		}
	}
}

// Ответ без нормального текста — сбой своего вида: потолок токенов,
// пусто, отказ модели, слишком большое тело или текст.
func TestBadOutput(t *testing.T) {
	huge := strings.Repeat("word ", maxTextBytes/5+1)
	hugeJSON, _ := json.Marshal(huge)
	cases := map[string][]struct {
		body string
		kind error
	}{
		ProviderAnthropic: {
			{`{"content":[{"type":"text","text":"Tom ran"}],"stop_reason":"max_tokens"}`, ErrTruncated},
			{`{"content":[{"type":"thinking","thinking":""}],"stop_reason":"end_turn"}`, ErrEmpty},
			{`{"content":[],"stop_reason":"refusal"}`, ErrRejected},
			{`{"content":[{"type":"text","text":` + string(hugeJSON) + `}],"stop_reason":"end_turn"}`, ErrTooLarge},
		},
		ProviderOpenAI: {
			{`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, ErrTruncated},
			{`{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`, ErrRejected},
			{`{"status":"completed","output":[{"type":"reasoning"}]}`, ErrEmpty},
			{`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":` + string(hugeJSON) + `}]}]}`, ErrTooLarge},
		},
		ProviderDeepSeek: {
			{`{"choices":[{"message":{"content":"Tom ran"},"finish_reason":"length"}]}`, ErrTruncated},
			{`{"choices":[{"message":{"content":""},"finish_reason":"content_filter"}]}`, ErrRejected},
			{`{"choices":[{"message":{"content":""},"finish_reason":"insufficient_system_resource"}]}`, ErrUnavailable},
			{`{"choices":[]}`, ErrEmpty},
			{`{"choices":[{"message":{"content":"  "},"finish_reason":"stop"}]}`, ErrEmpty},
		},
	}
	padding := `{"pad":"` + strings.Repeat("x", maxResponseBytes) + `"}`
	for name, list := range cases {
		list = append(list, struct {
			body string
			kind error
		}{padding, ErrTooLarge}, struct {
			body string
			kind error
		}{`not json`, ErrBadResponse})
		for _, c := range list {
			p := testProvider(t, name, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(c.body))
			})
			if _, err := p.Generate(context.Background(), testRequest); !errors.Is(err, c.kind) {
				t.Errorf("%s %.60s: %v, хотели %v", name, c.body, err, c.kind)
			}
		}
	}
}

// Зависший провайдер обрывается таймаутом клиента, отменённый запрос —
// контекстом; оба — временный сбой ErrTimeout.
func TestTimeout(t *testing.T) {
	for _, name := range Providers {
		p := testProvider(t, name, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(time.Second):
			case <-r.Context().Done():
			}
		})
		clientOf(p).http.Timeout = 50 * time.Millisecond
		start := time.Now()
		_, err := p.Generate(context.Background(), testRequest)
		if !errors.Is(err, ErrTimeout) || !Temporary(err) || time.Since(start) > 5*time.Second {
			t.Errorf("%s: таймаут клиента: %v", name, err)
		}
		clientOf(p).http.Timeout = Timeout
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err = p.Generate(ctx, testRequest)
		cancel()
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("%s: дедлайн контекста: %v", name, err)
		}
	}
}
