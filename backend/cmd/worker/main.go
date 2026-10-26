package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IEZhu/class/backend/internal/assemblyai"
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
	asr, err := asrFromEnv()
	if err != nil {
		log.Fatalf("worker: %v", err)
	}
	if asr.Client == nil {
		log.Printf("worker: ASSEMBLYAI_API_KEY не задан — уроки с записью уходят в done без транскрипта")
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
	worker.Register(runner, st, asr)

	log.Printf("worker: started")
	runner.Run(ctx)
	log.Printf("worker: stopped")
}

// asrFromEnv — транскрибация AssemblyAI (S2-1, ADR-015). Без ключа —
// нулевое значение: ASR выключен. С ключом нужны секрет вебхука,
// хранилище (ссылка на запись, выгрузка VTT) и DOMAIN для адреса
// вебхука; неполная настройка — ошибка старта, а не молчаливый пропуск.
func asrFromEnv() (worker.ASR, error) {
	client, err := assemblyai.New(assemblyai.Config{
		APIKey:        os.Getenv("ASSEMBLYAI_API_KEY"),
		WebhookSecret: os.Getenv("ASSEMBLYAI_WEBHOOK_SECRET"),
	})
	if err != nil || client == nil {
		return worker.ASR{}, err
	}
	files, err := storage.New(storage.ConfigFromEnv())
	if err != nil {
		return worker.ASR{}, err
	}
	if files == nil {
		return worker.ASR{}, errors.New("ASSEMBLYAI_API_KEY задан, а хранилище S3_* не настроено: AssemblyAI неоткуда взять запись")
	}
	domain := os.Getenv("DOMAIN")
	if domain == "" {
		return worker.ASR{}, errors.New("ASSEMBLYAI_API_KEY задан, а DOMAIN пуст: AssemblyAI некуда слать вебхук")
	}
	// Caddy стрипает /api, поэтому снаружи вебхук — /api/webhooks/assemblyai
	return worker.ASR{Client: client, Storage: files, WebhookURL: "https://" + domain + "/api/webhooks/assemblyai"}, nil
}
