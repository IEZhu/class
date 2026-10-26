// Команда dictimport — импорт словарного датасета (S3-4, ADR-019):
// JSONL Wiktextract с kaikki.org (URL https или локальный файл, gzip или
// нет) → таблица definitions. Разовая операция стенда, а не джоба: полный
// дамп разбирается дольше таймаута хендлера очереди (ADR-013). Повторный
// запуск продолжает оборванный прогон с контрольной точки.
//
//	dictimport -source https://kaikki.org/dictionary/raw-wiktextract-data.jsonl.gz
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IEZhu/class/backend/internal/dictionary"
	"github.com/IEZhu/class/backend/internal/dumpfile"
)

func main() {
	source := flag.String("source", "", "URL (https://) или путь к JSONL Wiktextract, можно .gz")
	restart := flag.Bool("restart", false, "начать незавершённый прогон заново")
	maxGlosses := flag.Int("max-glosses", dictionary.DefaultMaxGlosses, "значений на статью")
	maxTranslations := flag.Int("max-translations", dictionary.DefaultMaxTranslations, "русских переводов на статью")
	flag.Parse()
	if *source == "" {
		log.Fatal("dictimport: -source обязателен")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("dictimport: DATABASE_URL is required")
	}

	// SIGTERM/Ctrl-C: текущая пачка откатывается, контрольная точка
	// остаётся на последней зафиксированной
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		log.Fatalf("dictimport: %v", err)
	}
	// Пачка и финальная уборка устаревших статей дольше пятисекундного
	// потолка api, но зависнуть навсегда тоже не должны
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "120000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatalf("dictimport: %v", err)
	}
	defer pool.Close()

	src, version, err := dumpfile.Open(ctx, *source)
	if err != nil {
		log.Fatalf("dictimport: %v", err)
	}
	defer src.Close()

	started := time.Now()
	st, err := dictionary.Import(ctx, pool, src, dictionary.Options{
		Source: *source, SourceVersion: version, Restart: *restart,
		Limits: dictionary.Limits{MaxGlosses: *maxGlosses, MaxTranslations: *maxTranslations},
		Logf:   log.Printf,
	})
	if st.Resumed {
		log.Printf("dictimport: продолжен прогон %d со строки %d", st.ImportID, st.Skipped)
	}
	if err != nil {
		log.Fatalf("dictimport: прогон %d остановлен на строке %d: %v (повторный запуск продолжит)",
			st.ImportID, st.Lines, err)
	}
	log.Printf("dictimport: прогон %d завершён за %s: строк %d, записано статей %d, испорченных строк %d, удалено устаревших %d",
		st.ImportID, time.Since(started).Round(time.Second), st.Lines, st.Entries, st.Malformed, st.Removed)
}
