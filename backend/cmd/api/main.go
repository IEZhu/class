package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/IEZhu/class/backend/internal/httpapi"
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

	srv := &http.Server{
		Addr:              addr,
		Handler: httpapi.New(st, httpapi.Config{
			PublicBaseURL: "https://" + domain,
			// Ключи LiveKit опциональны: без них работает всё, кроме комнаты
			LiveKitURL:       os.Getenv("LIVEKIT_URL"),
			LiveKitAPIKey:    os.Getenv("LIVEKIT_API_KEY"),
			LiveKitAPISecret: os.Getenv("LIVEKIT_API_SECRET"),
		}).Router(),
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
