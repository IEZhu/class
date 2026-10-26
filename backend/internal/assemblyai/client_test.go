package assemblyai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	testKey    = "fixture-key"
	testSecret = "fixture-webhook-secret-0123456789abcdef"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{APIKey: testKey, WebhookSecret: testSecret, BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Без ключа транскрибация выключена и это не ошибка; ключ без годного
// секрета — ошибка: результат было бы нечем принять.
func TestNew(t *testing.T) {
	if c, err := New(Config{}); c != nil || err != nil {
		t.Errorf("без ключа: (%v, %v), хотели (nil, nil)", c, err)
	}
	if _, err := New(Config{APIKey: testKey}); !errors.Is(err, ErrNoWebhookSecret) {
		t.Errorf("без секрета: %v", err)
	}
	if _, err := New(Config{APIKey: testKey, WebhookSecret: strings.Repeat("s", MinWebhookSecretLen-1)}); err == nil {
		t.Error("короткий секрет принят")
	}
	c, err := New(Config{APIKey: testKey, WebhookSecret: testSecret})
	if err != nil || c.baseURL != DefaultBaseURL {
		t.Errorf("по умолчанию: %v, %v", c, err)
	}
}

// Тело запроса — по документации POST /v2/transcript: ключ в Authorization
// без схемы, диаризация, английский, явная модель, вебхук с секретом.
func TestSubmit(t *testing.T) {
	var got map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/transcript" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != testKey || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, `{"error":"Authentication error"}`, http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"id":"5551722-f677-48a7-9b4a-00ba4a6a8d62","status":"queued","audio_url":"x"}`))
	})
	id, err := c.Submit(context.Background(), "https://bucket.example/rec.mp4?X-Amz-Signature=abc", "https://class.example/api/webhooks/assemblyai")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if id != "5551722-f677-48a7-9b4a-00ba4a6a8d62" {
		t.Errorf("id = %q", id)
	}
	want := map[string]any{
		"audio_url":                 "https://bucket.example/rec.mp4?X-Amz-Signature=abc",
		"speech_models":             []any{"universal-2"},
		"language_code":             "en",
		"punctuate":                 true,
		"speaker_labels":            true,
		"webhook_url":               "https://class.example/api/webhooks/assemblyai",
		"webhook_auth_header_name":  WebhookHeader,
		"webhook_auth_header_value": testSecret,
	}
	if len(got) != len(want) {
		t.Errorf("поля запроса %v, хотели ровно %v", got, want)
	}
	for k, v := range want {
		if gotJSON, wantJSON := mustJSON(t, got[k]), mustJSON(t, v); gotJSON != wantJSON {
			t.Errorf("%s = %s, хотели %s", k, gotJSON, wantJSON)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Отказ API — ошибка с кодом и причиной из тела; причина обрезана, но
// остаётся валидным UTF-8 (её пишут в jobs.last_error).
func TestSubmitError(t *testing.T) {
	long := strings.Repeat("я", maxErrorBytes)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"x`+long+`"}`, http.StatusBadRequest)
	})
	_, err := c.Submit(context.Background(), "https://bucket.example/a", "https://class.example/hook")
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), `{"error":"x`) {
		t.Fatalf("err = %v", err)
	}
	if !utf8.ValidString(err.Error()) || len(err.Error()) > 2*maxErrorBytes {
		t.Fatalf("причина не обрезана или порвана: %d байт", len(err.Error()))
	}

	empty := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"queued"}`)) })
	if _, err := empty.Submit(context.Background(), "https://bucket.example/a", "https://class.example/hook"); err == nil {
		t.Fatal("ответ без id принят")
	}
}

// GET /v2/transcript/{id}: реплики со спикером и временем в мс; лишние
// поля ответа не мешают.
func TestTranscript(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/transcript/tr-1" || r.Header.Get("Authorization") != testKey {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":"tr-1","status":"completed","audio_duration":12,"text":"Hi. Hello.",
			"utterances":[{"speaker":"A","start":10,"end":900,"confidence":0.97,"text":"Hi.","words":[{"text":"Hi.","start":10,"end":900}]},
			              {"speaker":"B","start":1000,"end":2000,"confidence":0.9,"text":"Hello.","words":[]}]}`))
	})
	tr, err := c.Transcript(context.Background(), "tr-1")
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if tr.Status != StatusCompleted || len(tr.Utterances) != 2 {
		t.Fatalf("%+v", tr)
	}
	if u := tr.Utterances[1]; u != (Utterance{Speaker: "B", Start: 1000, End: 2000, Text: "Hello."}) {
		t.Errorf("реплика %+v", u)
	}

	failed := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"tr-2","status":"error","error":"Download error","utterances":null}`))
	})
	tr, err = failed.Transcript(context.Background(), "tr-2")
	if err != nil || tr.Status != StatusError || tr.Error != "Download error" || tr.Utterances != nil {
		t.Fatalf("%+v, %v", tr, err)
	}

	missing := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Transcript lookup error, transcript id not found"}`, http.StatusNotFound)
	})
	if _, err := missing.Transcript(context.Background(), "tr-3"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}

// id из базы уходит в путь экранированным: чужой путь из него не собрать.
func TestTranscriptEscapesID(t *testing.T) {
	var path string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"id":"x","status":"completed"}`))
	})
	if _, err := c.Transcript(context.Background(), "../account?x=1"); err != nil {
		t.Fatal(err)
	}
	if path != "/v2/transcript/..%2Faccount%3Fx=1" {
		t.Errorf("path = %q", path)
	}
}
