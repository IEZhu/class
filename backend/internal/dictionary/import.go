package dictionary

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IEZhu/class/backend/internal/dumpfile"
)

const (
	// DefaultBatchLines — строк источника на транзакцию. Контрольная точка
	// фиксируется вместе со статьями пачки, поэтому обрыв теряет не больше
	// одной пачки работы.
	DefaultBatchLines = 20000
	// maxLineBytes — строка длиннее считается испорченной и пропускается
	// (самые большие статьи Wiktextract — единицы мегабайт)
	maxLineBytes = 16 << 20
	// importLock — ключ pg_advisory_lock: два импорта одновременно
	// писали бы в один прогон
	importLock int64 = 0x5333_3464_6963_7431 // "S34dict1"
)

// Options — параметры прогона.
type Options struct {
	// Source — URL или путь источника: по нему находится незавершённый
	// прогон, который надо продолжить
	Source string
	// SourceVersion — версия источника (ETag/Last-Modified для URL, размер
	// и время изменения для файла). Продолжить можно только ту же версию:
	// иначе пропуск уже учтённых строк пропустил бы чужие статьи.
	SourceVersion string
	// Restart — начать незавершённый прогон заново, с первой строки
	Restart    bool
	BatchLines int
	Limits     Limits
	// Logf — прогресс; nil — молча
	Logf func(format string, args ...any)
}

// Stats — итог прогона.
type Stats struct {
	ImportID  int64
	Resumed   bool
	Lines     int64 // строк источника всего
	Skipped   int64 // уже учтены прошлой попыткой этого прогона
	Entries   int64 // статей записано за этот запуск
	Malformed int64 // строк не JSON или длиннее maxLineBytes
	Removed   int64 // статей прошлых импортов, которых нет в новом
}

// ErrBusy — другой импорт уже идёт.
var ErrBusy = errors.New("dictionary: импорт уже идёт")

// ErrEmpty — источник не дал ни одной статьи. Прогон не завершается:
// иначе завершение удалило бы весь прежний словарь.
var ErrEmpty = errors.New("dictionary: источник не дал ни одной статьи")

// Import читает JSONL Wiktextract (сжатый gzip или нет) и обновляет
// definitions. Повторный запуск на том же источнике даёт тот же словарь
// (upsert по заголовку, части речи и этимологии); оборванный прогон
// продолжается с последней зафиксированной пачки. Статьи прошлых
// импортов, которых нет в источнике, удаляются только по успешному
// завершению.
func Import(ctx context.Context, pool *pgxpool.Pool, src io.Reader, opt Options) (Stats, error) {
	opt = withDefaults(opt)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return Stats{}, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, importLock).Scan(&locked); err != nil {
		return Stats{}, err
	}
	if !locked {
		return Stats{}, ErrBusy
	}
	// Блокировка сессионная: вернуть соединение в пул с ней нельзя
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, importLock)
	}()

	st, err := beginRun(ctx, conn.Conn(), opt)
	if err != nil {
		return st, err
	}
	r, err := dumpfile.Decompress(src)
	if err != nil {
		return st, err
	}
	br := bufio.NewReaderSize(r, 1<<20)
	var batch []batchEntry
	var line int64
	var total int64 // статей прогона, включая прошлые попытки
	if err := conn.QueryRow(ctx, `SELECT entries FROM definition_imports WHERE id = $1`,
		st.ImportID).Scan(&total); err != nil {
		return st, err
	}
	flush := func() error {
		n, err := writeBatch(ctx, conn.Conn(), st.ImportID, line, batch)
		if err != nil {
			return err
		}
		st.Entries += n
		total += n
		batch = batch[:0]
		return nil
	}
	for {
		raw, tooLong, err := dumpfile.ReadLine(br, maxLineBytes)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return st, err
		}
		line++
		st.Lines = line
		if line <= st.Skipped {
			continue
		}
		if tooLong {
			st.Malformed++
		} else if e, ok, perr := parseEntry(raw, opt.Limits); perr != nil {
			st.Malformed++
		} else if ok {
			batch = append(batch, batchEntry{Entry: e, line: line})
		}
		if line%int64(opt.BatchLines) == 0 {
			if err := flush(); err != nil {
				return st, err
			}
			if line%(10*int64(opt.BatchLines)) == 0 {
				opt.logf("dictionary: %d строк, %d статей", line, total)
			}
		}
	}
	if err := flush(); err != nil {
		return st, err
	}
	if total == 0 {
		return st, ErrEmpty
	}
	st.Removed, err = finishRun(ctx, conn.Conn(), st.ImportID)
	return st, err
}

func withDefaults(opt Options) Options {
	if opt.BatchLines <= 0 {
		opt.BatchLines = DefaultBatchLines
	}
	if opt.Limits.MaxGlosses <= 0 {
		opt.Limits.MaxGlosses = DefaultMaxGlosses
	}
	if opt.Limits.MaxTranslations <= 0 {
		opt.Limits.MaxTranslations = DefaultMaxTranslations
	}
	return opt
}

func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// beginRun находит незавершённый прогон источника или заводит новый.
func beginRun(ctx context.Context, db *pgx.Conn, opt Options) (Stats, error) {
	if opt.Source == "" {
		return Stats{}, errors.New("dictionary: источник не задан")
	}
	var st Stats
	var version string
	err := db.QueryRow(ctx,
		`SELECT id, lines_done, source_version FROM definition_imports
		 WHERE source = $1 AND finished_at IS NULL`, opt.Source).Scan(&st.ImportID, &st.Skipped, &version)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = db.QueryRow(ctx,
			`INSERT INTO definition_imports (source, source_version) VALUES ($1, $2) RETURNING id`,
			opt.Source, opt.SourceVersion).Scan(&st.ImportID)
		return st, err
	case err != nil:
		return st, err
	case opt.Restart:
		st.Skipped = 0
		_, err = db.Exec(ctx,
			`UPDATE definition_imports SET lines_done = 0, entries = 0, source_version = $2 WHERE id = $1`,
			st.ImportID, opt.SourceVersion)
		return st, err
	case version != opt.SourceVersion:
		return st, fmt.Errorf("dictionary: незавершённый импорт %d начат на версии источника %q, сейчас %q: продолжить нельзя, запустите с -restart",
			st.ImportID, version, opt.SourceVersion)
	}
	st.Resumed = true
	return st, nil
}

type batchEntry struct {
	Entry
	line int64
}

// writeBatch — статьи пачки и контрольная точка одной транзакцией.
// Конфликт по (word, pos, etymology) внутри прогона оставляет первую
// статью источника; повторная обработка той же строки (рестарт)
// и статьи прошлых импортов перезаписываются.
func writeBatch(ctx context.Context, db *pgx.Conn, importID, linesDone int64, batch []batchEntry) (int64, error) {
	var written int64
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		b := &pgx.Batch{}
		for _, e := range batch {
			b.Queue(`INSERT INTO definitions
				  (word, lemma, pos, etymology, glosses, translations_ru, source_line, import_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				ON CONFLICT (word, pos, etymology) DO UPDATE SET
				  lemma = EXCLUDED.lemma, glosses = EXCLUDED.glosses,
				  translations_ru = EXCLUDED.translations_ru,
				  source_line = EXCLUDED.source_line, import_id = EXCLUDED.import_id
				WHERE definitions.import_id <> EXCLUDED.import_id
				   OR definitions.source_line >= EXCLUDED.source_line`,
				e.Word, e.Lemma, e.Pos, e.Etymology, e.Glosses, nonNil(e.TranslationsRU), e.line, importID,
			).Exec(func(tag pgconn.CommandTag) error {
				written += tag.RowsAffected()
				return nil
			})
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`UPDATE definition_imports SET lines_done = $2, entries = entries + $3 WHERE id = $1`,
			importID, linesDone, written)
		return err
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

// finishRun удаляет статьи, которых не было в источнике, и закрывает прогон.
func finishRun(ctx context.Context, db *pgx.Conn, importID int64) (int64, error) {
	var removed int64
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM definitions WHERE import_id <> $1`, importID)
		if err != nil {
			return err
		}
		removed = tag.RowsAffected()
		_, err = tx.Exec(ctx, `UPDATE definition_imports SET finished_at = now() WHERE id = $1`, importID)
		return err
	})
	return removed, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
