package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/IEZhu/class/backend/internal/gcal"
	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/worker"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("worker: DATABASE_URL is required")
	}

	// docker stop шлёт SIGTERM: новые джобы больше не берём, текущую
	// доводим или возвращаем в очередь (jobs.Runner.Run)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := store.New(connectCtx, databaseURL)
	cancel()
	if err != nil {
		log.Fatalf("worker: store: %v", err)
	}
	defer st.Close()

	runner := jobs.NewRunner(st.Pool(), jobs.Config{})
	worker.Register(runner, st)

	// S1-1: Google Calendar опционален — без ключей calendar_sync снимает
	// джобы со строкой в журнале (ADR-023)
	googleCfg := gcal.Config{
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		TokenKey:     os.Getenv("GOOGLE_TOKEN_KEY"),
	}
	cal, err := gcal.New(googleCfg)
	if err != nil {
		log.Fatalf("worker: google: %v", err)
	}
	domain := os.Getenv("DOMAIN")
	switch {
	case cal == nil:
		log.Printf("worker: Google Calendar выключен: не заданы %s", strings.Join(googleCfg.Missing(), ", "))
	case domain == "":
		// Ссылка на урок в описании события собирается из домена стенда
		log.Fatal("worker: DOMAIN is required for calendar_sync")
	}
	worker.RegisterCalendar(runner, st, cal, "https://"+domain)

	log.Printf("worker: started")
	runner.Run(ctx)
	log.Printf("worker: stopped")
}
