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
	"github.com/IEZhu/class/backend/internal/harvester"
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
		registerCore(runner, st)
		// F1: уроки, которые egress_ended сам не завершил (ADR-031)
		worker.RegisterRecordingSweep(runner, scheduler, st)
		backfillSRSCards(ctx, st)
		// D-15: протухшие сессии всех учёток, в том числе гостей (ADR-035)
		worker.RegisterSessionCleanup(runner, scheduler, st)
	}
	go scheduler.Run(ctx)

	log.Printf("worker: started, role %s", role)
	runner.Run(ctx)
	log.Printf("worker: stopped")
}

// backfillSRSCards — карточки FSRS словам словаря без карточки (S4-1,
// долг D-3, ADR-036): словам до миграции 0021 и добавленным старым api,
// пока шёл выкат. Идемпотентно, поэтому на каждом старте; сбой (миграция
// ещё не применена) — строка в журнале, остальные джобы работают.
// Ненулевое число после первого выката — путь записи слов в обход
// store.SaveUserTerm.
func backfillSRSCards(ctx context.Context, st *store.Store) {
	n, err := st.BackfillSRSCards(ctx)
	switch {
	case err != nil:
		log.Printf("worker: карточки FSRS не заведены: %v", err)
	case n > 0:
		log.Printf("worker: заведено карточек FSRS для слов без карточки: %d", n)
	}
}

// registerCore — джобы стенда (роль core). Ключи интеграций читает только
// эта роль: harvester с другой машины обходится одним DATABASE_URL.
func registerCore(runner *jobs.Runner, st *store.Store) {
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
	worker.Register(runner, st, asr)

	// S1-1: Google Calendar опционален — без ключей calendar_sync снимает
	// создание и перенос со строкой в журнале, удаления ждут (ADR-023)
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

	// S3-6: снимки досок пишутся в то же хранилище, что и записи (ADR-020);
	// долг D-4: файлы материалов удаляются из того же хранилища, что у api
	// (ADR-024). Без ключей обе джобы ждут в очереди, остальное работает
	if objects != nil {
		worker.RegisterWhiteboard(runner, st, objects)
		worker.RegisterMaterialFiles(runner, objects)
	} else {
		log.Printf("worker: хранилище не настроено — снимки досок и удаление файлов материалов ждут в очереди")
	}

	// S3-7: слова доски кончившегося урока — в кандидаты словаря урока
	// (ADR-033); хранилище не нужно, сцена лежит в Postgres
	worker.RegisterLessonTerms(runner, st)
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
