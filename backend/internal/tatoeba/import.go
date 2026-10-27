package tatoeba

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IEZhu/class/backend/internal/dumpfile"
)

const (
	// DefaultBatchLines — строк выгрузки на транзакцию. Контрольная точка
	// фиксируется вместе с предложениями пачки, поэтому обрыв теряет не
	// больше одной пачки работы.
	DefaultBatchLines = 20000
	// maxLineBytes — строка длиннее считается испорченной: предложения
	// Tatoeba — сотни байт
	maxLineBytes = 64 << 10
	// importLock — ключ pg_advisory_lock: два импорта одновременно писали
	// бы в один прогон
	importLock int64 = 0x5335_3474_6174_6f65 // "S54tatoe"
)

// Фазы прогона (tatoeba_imports.phase). Пары переводов при каждом запуске
// читаются в память целиком, затем пишутся русские предложения из пар,
// затем английские вместе с парами: к английскому предложению его русские
// переводы уже известны.
const (
	phaseRussian = "ru"
	phaseEnglish = "en"
)

// Sources — три файла выгрузки, сжатые bzip2 (как на tatoeba.org), gzip
// или нет.
type Sources struct {
	Links   io.Reader // eng-rus_links.tsv: id английского, id русского
	English io.Reader // eng_sentences_detailed.tsv
	Russian io.Reader // rus_sentences_detailed.tsv
	// Version — версии трёх файлов (ETag или Last-Modified у URL, размер
	// и mtime у файла). Продолжить незавершённый прогон можно только на
	// той же версии и тех же потолках: иначе пропуск учтённых строк
	// пропустил бы чужие предложения.
	Version string
}

// Options — параметры прогона.
type Options struct {
	// Restart — бросить незавершённый прогон и начать новый
	Restart    bool
	BatchLines int
	Limits     Limits
	// Logf — прогресс; nil — молча
	Logf func(format string, args ...any)
}

// Stats — итог запуска.
type Stats struct {
	ImportID int64
	Resumed  bool
	Phase    string // фаза, с которой начат этот запуск
	Skipped  int64  // строк этой фазы учтено прошлой попыткой
	Pairs    int64  // пар eng-rus в выгрузке
	// Malformed — испорченных строк в прочитанной части трёх файлов
	Malformed int64
	// Записано за этот запуск
	Russian, English, Links int64
	// Удалено при завершении: прошлых импортов и русских предложений,
	// которые не стали переводом
	RemovedSentences, RemovedLinks int64
	// Корпус после завершения
	TotalEnglish, TotalRussian, TotalLinks int64
}

// ErrBusy — другой импорт уже идёт.
var ErrBusy = errors.New("tatoeba: импорт уже идёт")

// ErrEmpty — выгрузка не дала ни одного английского предложения
// с переводом. Прогон не завершается: иначе завершение удалило бы весь
// прежний корпус.
var ErrEmpty = errors.New("tatoeba: в выгрузке нет английских предложений с русским переводом")

// Import обновляет корпус из выгрузки Tatoeba. Повторный запуск на той же
// выгрузке даёт тот же корпус (upsert по id предложения и паре);
// оборванный прогон продолжается с последней зафиксированной пачки своей
// фазы. Предложения и пары прошлых импортов, которых нет в новом, и
// русские предложения, не ставшие ничьим переводом, удаляются только
// при успешном завершении. До него в таблице могут лежать русские
// предложения без пары: запросы корпуса идут через пары и их не видят.
func Import(ctx context.Context, pool *pgxpool.Pool, src Sources, opt Options) (Stats, error) {
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

	db := conn.Conn()
	version := fmt.Sprintf("%s max-runes=%d max-translations=%d",
		src.Version, opt.Limits.MaxRunes, opt.Limits.MaxTranslations)
	st, err := beginRun(ctx, db, version, opt.Restart)
	if err != nil {
		return st, err
	}
	r := &run{db: db, opt: opt, st: &st}
	if r.links, err = r.readLinks(src.Links); err != nil {
		return st, err
	}
	skip := st.Skipped
	if st.Phase == phaseRussian {
		if err := r.importRussian(ctx, src.Russian, skip); err != nil {
			return st, err
		}
		if _, err := db.Exec(ctx, `UPDATE tatoeba_imports SET phase = $2, lines_done = 0 WHERE id = $1`,
			st.ImportID, phaseEnglish); err != nil {
			return st, err
		}
		skip = 0
	}
	if err := r.importEnglish(ctx, src.English, skip); err != nil {
		return st, err
	}
	return st, r.finish(ctx)
}

func withDefaults(opt Options) Options {
	if opt.BatchLines <= 0 {
		opt.BatchLines = DefaultBatchLines
	}
	if opt.Limits.MaxRunes <= 0 {
		opt.Limits.MaxRunes = DefaultMaxRunes
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

// beginRun находит незавершённый прогон или заводит новый. Незавершённый
// прогон один на корпус (tatoeba_imports_running_key).
func beginRun(ctx context.Context, db *pgx.Conn, version string, restart bool) (Stats, error) {
	var st Stats
	var was string
	err := db.QueryRow(ctx,
		`SELECT id, phase, lines_done, source_version FROM tatoeba_imports
		WHERE finished_at IS NULL AND abandoned_at IS NULL`).
		Scan(&st.ImportID, &st.Phase, &st.Skipped, &was)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return newRun(ctx, db, version)
	case err != nil:
		return st, err
	case restart:
		// Новый прогон, а не прежний с первой фазы: записанное брошенным
		// (другая выгрузка, другие потолки) остаётся под его id и уйдёт при
		// завершении нового, если тот его не перезапишет
		if _, err := db.Exec(ctx, `UPDATE tatoeba_imports SET abandoned_at = now() WHERE id = $1`,
			st.ImportID); err != nil {
			return st, err
		}
		return newRun(ctx, db, version)
	case was != version:
		return st, fmt.Errorf("tatoeba: незавершённый импорт %d начат на версии выгрузки %q, сейчас %q: продолжить нельзя, запустите с -restart",
			st.ImportID, was, version)
	}
	st.Resumed = true
	return st, nil
}

func newRun(ctx context.Context, db *pgx.Conn, version string) (Stats, error) {
	st := Stats{Phase: phaseRussian}
	err := db.QueryRow(ctx,
		`INSERT INTO tatoeba_imports (source_version, phase) VALUES ($1, $2) RETURNING id`,
		version, st.Phase).Scan(&st.ImportID)
	return st, err
}

type run struct {
	db    *pgx.Conn
	opt   Options
	st    *Stats
	links linkIndex
}

// linkIndex — пары выгрузки в памяти: ~830 тыс. пар — около 20 MB.
type linkIndex struct {
	byEn []pair  // по (en, ru), без повторов
	ru   []int64 // русские id из пар, по возрастанию, без повторов
}

// translations — русские id пар английского предложения, по возрастанию.
func (x linkIndex) translations(en int64) []pair {
	i, _ := slices.BinarySearchFunc(x.byEn, en, func(p pair, en int64) int { return cmp.Compare(p.en, en) })
	j := i
	for j < len(x.byEn) && x.byEn[j].en == en {
		j++
	}
	return x.byEn[i:j]
}

func (r *run) readLinks(src io.Reader) (linkIndex, error) {
	var x linkIndex
	err := scanLines(src, 0, func(raw []byte) error {
		p, err := parseLink(raw)
		if err != nil {
			return err
		}
		x.byEn = append(x.byEn, p)
		return nil
	}, nil, r.opt.BatchLines, &r.st.Malformed)
	if err != nil {
		return x, err
	}
	slices.SortFunc(x.byEn, func(a, b pair) int { return cmp.Or(cmp.Compare(a.en, b.en), cmp.Compare(a.ru, b.ru)) })
	x.byEn = slices.Compact(x.byEn)
	x.ru = make([]int64, len(x.byEn))
	for i, p := range x.byEn {
		x.ru[i] = p.ru
	}
	slices.Sort(x.ru)
	x.ru = slices.Compact(x.ru)
	r.st.Pairs = int64(len(x.byEn))
	return x, nil
}

// importRussian пишет русские предложения, которые входят в пары и
// укладываются в потолок длины. Какие из них станут переводом, решает
// английская фаза; остальные удалит завершение прогона.
func (r *run) importRussian(ctx context.Context, src io.Reader, skip int64) error {
	b := newBatch()
	return scanLines(src, skip, func(raw []byte) error {
		s, err := parseSentence(raw, "rus")
		if err != nil {
			return err
		}
		if _, linked := slices.BinarySearch(r.links.ru, s.ID); linked && r.fits(s.Text) {
			b.add(row{sentence: s})
		}
		return nil
	}, func(linesDone int64) error {
		return r.flush(ctx, phaseRussian, linesDone, b)
	}, r.opt.BatchLines, &r.st.Malformed)
}

// importEnglish пишет английские предложения с переводом: до
// MaxTranslations пар с русскими предложениями этого прогона, младшие id
// первыми. Предложение без слов (числа, знаки) примером не станет.
func (r *run) importEnglish(ctx context.Context, src io.Reader, skip int64) error {
	rows, err := r.db.Query(ctx,
		`SELECT id FROM tatoeba_sentences WHERE lang = 'ru' AND import_id = $1 ORDER BY id`, r.st.ImportID)
	if err != nil {
		return err
	}
	russian, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	b := newBatch()
	return scanLines(src, skip, func(raw []byte) error {
		s, err := parseSentence(raw, "eng")
		if err != nil {
			return err
		}
		if !r.fits(s.Text) {
			return nil
		}
		var ru []int64
		for _, p := range r.links.translations(s.ID) {
			if _, ok := slices.BinarySearch(russian, p.ru); ok {
				ru = append(ru, p.ru)
				if len(ru) == r.opt.Limits.MaxTranslations {
					break
				}
			}
		}
		if len(ru) == 0 {
			return nil
		}
		if lemmas := Lemmas(s.Text); len(lemmas) > 0 {
			b.add(row{sentence: s, lemmas: lemmas, ru: ru})
		}
		return nil
	}, func(linesDone int64) error {
		return r.flush(ctx, phaseEnglish, linesDone, b)
	}, r.opt.BatchLines, &r.st.Malformed)
}

func (r *run) fits(text string) bool {
	return utf8.RuneCountInString(text) <= r.opt.Limits.MaxRunes
}

// row — предложение пачки; у английского — леммы и выбранные переводы.
type row struct {
	sentence
	lemmas []string
	ru     []int64
}

// batch — предложения пачки по id: повтор id в выгрузке перезаписывает
// предыдущую строку, как и upsert между пачками.
type batch struct {
	rows []row
	at   map[int64]int
}

func newBatch() *batch { return &batch{at: map[int64]int{}} }

func (b *batch) add(r row) {
	if i, ok := b.at[r.ID]; ok {
		b.rows[i] = r
		return
	}
	b.at[r.ID] = len(b.rows)
	b.rows = append(b.rows, r)
}

func (b *batch) reset() {
	b.rows = b.rows[:0]
	clear(b.at)
}

// flush — предложения и пары пачки вместе с контрольной точкой одной
// транзакцией.
func (r *run) flush(ctx context.Context, phase string, linesDone int64, b *batch) error {
	var sentences, links int64
	err := pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		if len(b.rows) > 0 {
			ids := make([]int64, len(b.rows))
			texts := make([]string, len(b.rows))
			authors := make([]string, len(b.rows))
			lemmas := make([]*string, len(b.rows))
			var en, ru []int64
			for i, row := range b.rows {
				ids[i], texts[i], authors[i] = row.ID, row.Text, row.Author
				if row.lemmas != nil {
					joined := strings.Join(row.lemmas, " ")
					lemmas[i] = &joined
				}
				for _, id := range row.ru {
					en, ru = append(en, row.ID), append(ru, id)
				}
			}
			// Леммы — слова без пробелов (lemma.IsWord), поэтому едут
			// одной строкой: unnest не разворачивает массив массивов
			tag, err := tx.Exec(ctx, `INSERT INTO tatoeba_sentences (id, lang, text, author, lemmas, import_id)
				SELECT t.id, $1, t.text, t.author, string_to_array(t.lemmas, ' '), $2
				FROM unnest($3::bigint[], $4::text[], $5::text[], $6::text[]) AS t(id, text, author, lemmas)
				ON CONFLICT (id) DO UPDATE SET
				  lang = EXCLUDED.lang, text = EXCLUDED.text, author = EXCLUDED.author,
				  lemmas = EXCLUDED.lemmas, import_id = EXCLUDED.import_id`,
				phase, r.st.ImportID, ids, texts, authors, lemmas)
			if err != nil {
				return err
			}
			sentences = tag.RowsAffected()
			if len(en) > 0 {
				tag, err = tx.Exec(ctx, `INSERT INTO tatoeba_links (en_id, ru_id, import_id)
					SELECT t.en, t.ru, $1 FROM unnest($2::bigint[], $3::bigint[]) AS t(en, ru)
					ON CONFLICT (en_id, ru_id) DO UPDATE SET import_id = EXCLUDED.import_id`,
					r.st.ImportID, en, ru)
				if err != nil {
					return err
				}
				links = tag.RowsAffected()
			}
		}
		_, err := tx.Exec(ctx, `UPDATE tatoeba_imports SET phase = $2, lines_done = $3 WHERE id = $1`,
			r.st.ImportID, phase, linesDone)
		return err
	})
	if err != nil {
		return err
	}
	if phase == phaseRussian {
		r.st.Russian += sentences
	} else {
		r.st.English += sentences
	}
	r.st.Links += links
	b.reset()
	if linesDone%(10*int64(r.opt.BatchLines)) == 0 {
		r.opt.logf("tatoeba: фаза %s, %d строк", phase, linesDone)
	}
	return nil
}

// finish удаляет пары и предложения прошлых импортов и русские
// предложения без пары, затем закрывает прогон.
func (r *run) finish(ctx context.Context) error {
	var english int64
	if err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM tatoeba_sentences WHERE lang = 'en' AND import_id = $1`,
		r.st.ImportID).Scan(&english); err != nil {
		return err
	}
	if english == 0 {
		return ErrEmpty
	}
	return pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM tatoeba_links WHERE import_id <> $1`, r.st.ImportID)
		if err != nil {
			return err
		}
		r.st.RemovedLinks = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `DELETE FROM tatoeba_sentences s
			WHERE s.import_id <> $1
			   OR (s.lang = 'ru' AND NOT EXISTS (SELECT 1 FROM tatoeba_links l WHERE l.ru_id = s.id))`,
			r.st.ImportID)
		if err != nil {
			return err
		}
		r.st.RemovedSentences = tag.RowsAffected()
		if _, err := tx.Exec(ctx, `UPDATE tatoeba_imports SET finished_at = now() WHERE id = $1`,
			r.st.ImportID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT
			  (SELECT count(*) FROM tatoeba_sentences WHERE lang = 'en'),
			  (SELECT count(*) FROM tatoeba_sentences WHERE lang = 'ru'),
			  (SELECT count(*) FROM tatoeba_links)`).
			Scan(&r.st.TotalEnglish, &r.st.TotalRussian, &r.st.TotalLinks)
	})
}

// scanLines читает выгрузку построчно: первые skip строк пропускает
// (учтены прошлой попыткой), остальные отдаёт handle; каждые batchLines
// строк и в конце файла вызывает flush с номером последней строки.
// Испорченная строка (errMalformed или длиннее maxLineBytes) только
// считается.
func scanLines(src io.Reader, skip int64, handle func(raw []byte) error,
	flush func(linesDone int64) error, batchLines int, malformed *int64) error {
	r, err := dumpfile.Decompress(src)
	if err != nil {
		return err
	}
	br := bufio.NewReaderSize(r, 1<<16)
	var line int64
	for {
		raw, tooLong, err := dumpfile.ReadLine(br, maxLineBytes)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		line++
		if line <= skip {
			continue
		}
		if tooLong {
			*malformed++
		} else if err := handle(raw); errors.Is(err, errMalformed) {
			*malformed++
		} else if err != nil {
			return err
		}
		if flush != nil && line%int64(batchLines) == 0 {
			if err := flush(line); err != nil {
				return err
			}
		}
	}
	if flush != nil {
		return flush(line)
	}
	return nil
}
