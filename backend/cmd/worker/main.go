package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IEZhu/class/backend/internal/harvester"
	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/worker"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("worker: DATABASE_URL is required")
	}
	// S5-2 (ADR-022): core — джобы стенда; harvester — только сборщик
	// субтитров (образ с yt-dlp), в том числе с другой машины в ту же базу
	role := os.Getenv("WORKER_ROLE")
	if role == "" {
		role = "core"
	}
	if role != "core" && role != "harvester" {
		log.Fatalf("worker: WORKER_ROLE %q: expected core or harvester", role)
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

	scheduler := jobs.NewScheduler(st.Pool())
	var runner *jobs.Runner
	if role == "harvester" {
		runner = jobs.NewRunner(st.Pool(), jobs.Config{HandlerTimeout: harvester.JobTimeout})
		worker.RegisterHarvester(runner, scheduler, harvester.New(st, harvester.Config{
			Fetcher: harvester.YTDLP{Cookies: os.Getenv("HARVESTER_COOKIES")},
		}))
	} else {
		runner = jobs.NewRunner(st.Pool(), jobs.Config{})
		worker.Register(runner, st)
	}
	go scheduler.Run(ctx)

	log.Printf("worker: started, role %s", role)
	runner.Run(ctx)
	log.Printf("worker: stopped")
}
