package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/storage"
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

	// Долг D-4: файлы материалов удаляются из того же хранилища, что
	// у api (ADR-024). Без ключей их джобы ждут в очереди
	objects, err := storage.New(storage.Config{
		Bucket:    os.Getenv("S3_BUCKET"),
		Region:    os.Getenv("S3_REGION"),
		Endpoint:  os.Getenv("S3_ENDPOINT"),
		AccessKey: os.Getenv("S3_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
	})
	if err != nil {
		log.Fatalf("worker: storage: %v", err)
	}
	if objects != nil {
		worker.RegisterMaterialFiles(runner, objects)
	} else {
		log.Printf("worker: хранилище не настроено — удаление файлов материалов ждёт в очереди")
	}

	log.Printf("worker: started")
	runner.Run(ctx)
	log.Printf("worker: stopped")
}
