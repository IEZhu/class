package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/IEZhu/class/backend/internal/gcal"
	"github.com/IEZhu/class/backend/internal/httpapi"
	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

func main() {
	addr := ":8080"
	if v := os.Getenv("API_ADDR"); v != "" {
		addr = v
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("api: DATABASE_URL is required")
	}
	// Нужен для ссылок-приглашений (ADR-008); ключ обязателен в deploy/.env
	domain := os.Getenv("DOMAIN")
	if domain == "" {
		log.Fatal("api: DOMAIN is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := store.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("api: store: %v", err)
	}
	defer st.Close()

	googleCfg := gcal.Config{
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		TokenKey:     os.Getenv("GOOGLE_TOKEN_KEY"),
	}
	api, err := httpapi.New(st, httpapi.Config{
		PublicBaseURL: "https://" + domain,
		// Ключи интеграций опциональны: без них работает всё, кроме
		// комнаты, записи и транскрипта
		LiveKitURL:       os.Getenv("LIVEKIT_URL"),
		LiveKitAPIKey:    os.Getenv("LIVEKIT_API_KEY"),
		LiveKitAPISecret: os.Getenv("LIVEKIT_API_SECRET"),
		EgressAudioOnly:  os.Getenv("EGRESS_AUDIO_ONLY") == "1",
		Storage:          storage.ConfigFromEnv(),
		// S1-1: без ключей подключение календаря отвечает 503 (ADR-023)
		Google: googleCfg,
		// S2-2: без секрета вебхук AssemblyAI отвечает 503
		AssemblyAIWebhookSecret: os.Getenv("ASSEMBLYAI_WEBHOOK_SECRET"),
		LibreTranslateURL:       os.Getenv("LIBRETRANSLATE_URL"),
	})
	if err != nil {
		log.Fatalf("api: httpapi: %v", err)
	}
	if !api.GoogleConfigured() {
		log.Printf("api: Google Calendar выключен: не заданы %s", strings.Join(googleCfg.Missing(), ", "))
	}
	if os.Getenv("LIBRETRANSLATE_URL") == "" {
		log.Printf("api: LIBRETRANSLATE_URL пуст — клик по слову отвечает без перевода")
	}
	apiHandler := api.Router()

	srv := &http.Server{
		Addr:    addr,
		Handler: apiHandler,

		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Printf("api: listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
