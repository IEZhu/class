package translate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewWithoutURL(t *testing.T) {
	if NewLibreTranslate("") != nil {
		t.Fatal("без адреса клиента быть не должно: перевод — опция стенда")
	}
}

// Запрос — по контракту LibreTranslate: POST /translate, JSON с q,
// source, target и format=text.
func TestTranslate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/translate" ||
			r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		var req translateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			req != (translateRequest{Q: "run", Source: "en", Target: "ru", Format: "text"}) {
			http.Error(w, `{"error":"unexpected body"}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"translatedText":" бежать "}`))
	}))
	defer srv.Close()
	got, err := NewLibreTranslate(srv.URL+"/").Translate(context.Background(), "run", "en", "ru")
	if err != nil || got != "бежать" {
		t.Fatalf("Translate = %q, %v", got, err)
	}
}

func TestTranslateErrors(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"ошибка сервиса": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"ru is not supported"}`))
		},
		"не JSON": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<html>`))
		},
		"пустой перевод": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"translatedText":""}`))
		},
		"нет ответа": func(w http.ResponseWriter, r *http.Request) {
			// Обрыв клиента сервер замечает только после чтения тела
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			got, err := NewLibreTranslate(srv.URL).Translate(ctx, "run", "en", "ru")
			if err == nil || got != "" {
				t.Fatalf("Translate = %q, %v; ждали ошибку", got, err)
			}
			if !strings.HasPrefix(err.Error(), "libretranslate:") {
				t.Fatalf("ошибка без префикса интеграции: %v", err)
			}
		})
	}
}
