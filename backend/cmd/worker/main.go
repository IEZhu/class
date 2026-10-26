package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/IEZhu/class/backend/internal/assemblyai"
	"github.com/IEZhu/class/backend/internal/gcal"
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
	// Хранилище S3_* одно на транскрипты (S2-1) и снимки досок (S3-6);
	// без ключей — nil, и зависящие от него джобы выключены
	objects, err := storage.New(storage.ConfigFromEnv())
	if err != nil {
		log.Fatalf("worker: storage: %v", err)
	}
	asr, err := asrFromEnv(objects)
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

	// S3-6: снимки досок пишутся в то же хранилище, что и записи (ADR-020).
	// Без ключей джобы снимков ждут в очереди, остальное работает
	if objects != nil {
		worker.RegisterWhiteboard(runner, st, objects)
	} else {
		log.Printf("worker: хранилище не настроено — снимки досок ждут в очереди")
	}

	log.Printf("worker: started")
	runner.Run(ctx)
	log.Printf("worker: stopped")
}

// asrFromEnv — транскрибация AssemblyAI (S2-1, ADR-015). Без ключа —
// нулевое значение: ASR выключен. С ключом нужны секрет вебхука,
// хранилище (ссылка на запись, выгрузка VTT) и DOMAIN для адреса
// вебхука; неполная настройка — ошибка старта, а не молчаливый пропуск.
func asrFromEnv(files *storage.Client) (worker.ASR, error) {
	client, err := assemblyai.New(assemblyai.Config{
		APIKey:        os.Getenv("ASSEMBLYAI_API_KEY"),
		WebhookSecret: os.Getenv("ASSEMBLYAI_WEBHOOK_SECRET"),
	})
	if err != nil || client == nil {
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
