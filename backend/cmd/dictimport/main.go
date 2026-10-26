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
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IEZhu/class/backend/internal/dictionary"
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

	src, version, err := open(ctx, *source)
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

// open открывает источник и возвращает его версию: по ней импорт решает,
// можно ли продолжить незавершённый прогон.
func open(ctx context.Context, source string) (io.ReadCloser, string, error) {
	if strings.HasPrefix(source, "http://") {
		return nil, "", fmt.Errorf("только https: словарь не должен подменяться по дороге")
	}
	if !strings.HasPrefix(source, "https://") {
		f, err := os.Open(source)
		if err != nil {
			return nil, "", err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, "", err
		}
		return f, fmt.Sprintf("size=%d mtime=%d", fi.Size(), fi.ModTime().Unix()), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, "", err
	}
	// Без общего таймаута: полный дамп качается минутами. Зависание
	// соединения ловят таймауты установки и первого ответа.
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: time.Minute,
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("GET %s: HTTP %d", source, resp.StatusCode)
	}
	version := resp.Header.Get("ETag")
	if version == "" {
		version = resp.Header.Get("Last-Modified")
	}
	return resp.Body, version, nil
}
