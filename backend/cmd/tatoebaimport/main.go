// Команда tatoebaimport — импорт корпуса Tatoeba (S5-4, ADR-028):
// поязыковые выгрузки tatoeba.org (пары eng-rus, подробные английские
// и русские предложения; URL https или локальные файлы, bzip2, gzip или
// без сжатия) → tatoeba_sentences и tatoeba_links. Разовая операция
// стенда, как dictimport: повторный запуск продолжает оборванный прогон
// с контрольной точки, а на свежей выгрузке обновляет корпус.
//
//	tatoebaimport   # официальные выгрузки по умолчанию
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IEZhu/class/backend/internal/dumpfile"
	"github.com/IEZhu/class/backend/internal/tatoeba"
)

func main() {
	links := flag.String("links", tatoeba.DefaultLinks, "URL (https://) или путь к eng-rus_links.tsv(.bz2)")
	english := flag.String("english", tatoeba.DefaultEnglish, "URL или путь к eng_sentences_detailed.tsv(.bz2)")
	russian := flag.String("russian", tatoeba.DefaultRussian, "URL или путь к rus_sentences_detailed.tsv(.bz2)")
	restart := flag.Bool("restart", false, "начать незавершённый прогон заново")
	maxRunes := flag.Int("max-runes", tatoeba.DefaultMaxRunes, "предложения длиннее (в символах) не берутся")
	maxTranslations := flag.Int("max-translations", tatoeba.DefaultMaxTranslations, "русских переводов на английское предложение")
	flag.Parse()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("tatoebaimport: DATABASE_URL is required")
	}

	// SIGTERM/Ctrl-C: текущая пачка откатывается, контрольная точка
	// остаётся на последней зафиксированной
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		log.Fatalf("tatoebaimport: %v", err)
	}
	// Пачка и уборка при завершении дольше пятисекундного потолка api,
	// но зависнуть навсегда тоже не должны
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "120000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatalf("tatoebaimport: %v", err)
	}
	defer pool.Close()

	// Версия выгрузки — версии трёх файлов: продолжить прогон можно,
	// только если не сменился ни один
	var versions []string
	var opened []io.Closer
	defer func() {
		for _, f := range opened {
			f.Close()
		}
	}()
	open := func(source string) io.Reader {
		f, version, err := dumpfile.Open(ctx, source)
		if err != nil {
			log.Fatalf("tatoebaimport: %v", err)
		}
		versions = append(versions, version)
		opened = append(opened, f)
		return f
	}
	src := tatoeba.Sources{Links: open(*links), English: open(*english), Russian: open(*russian)}
	src.Version = strings.Join(versions, " | ")

	started := time.Now()
	st, err := tatoeba.Import(ctx, pool, src, tatoeba.Options{
		Restart: *restart,
		Limits:  tatoeba.Limits{MaxRunes: *maxRunes, MaxTranslations: *maxTranslations},
		Logf:    log.Printf,
	})
	if st.Resumed {
		log.Printf("tatoebaimport: продолжен прогон %d с фазы %s, строки %d", st.ImportID, st.Phase, st.Skipped)
	}
	if err != nil {
		log.Fatalf("tatoebaimport: прогон %d остановлен: %v (повторный запуск продолжит)", st.ImportID, err)
	}
	log.Printf("tatoebaimport: прогон %d завершён за %s: пар в выгрузке %d, испорченных строк %d; записано русских %d, английских %d, пар %d; удалено предложений %d, пар %d; в корпусе английских %d, русских %d, пар %d",
		st.ImportID, time.Since(started).Round(time.Second), st.Pairs, st.Malformed,
		st.Russian, st.English, st.Links, st.RemovedSentences, st.RemovedLinks,
		st.TotalEnglish, st.TotalRussian, st.TotalLinks)
}
