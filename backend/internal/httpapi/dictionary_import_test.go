package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/dictionary"
)

// Импорт словаря (S3-4) проверяется здесь, а не в пакете dictionary:
// база у тестов одна — изолированная схема securityDB с настоящими
// миграциями.

const dictionaryFixture = "../dictionary/testdata/wiktextract-sample.jsonl"

func importFixture(t *testing.T, a *API, path string, opt dictionary.Options) dictionary.Stats {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := dictionary.Import(context.Background(), a.store.Pool(), f, opt)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// definitionsSnapshot — содержимое definitions без служебного import_id.
func definitionsSnapshot(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	rows, err := conn.Query(context.Background(),
		`SELECT word, lemma, pos, etymology, glosses, translations_ru, source_line
		 FROM definitions ORDER BY word, pos, etymology`)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&b, vals...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// failingReader отдаёт первые n байт источника и обрывается ошибкой —
// как упавшая на середине загрузка.
type failingReader struct {
	r io.Reader
	n int
}

var errCut = errors.New("соединение оборвалось")

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errCut
	}
	if len(p) > f.n {
		p = p[:f.n]
	}
	n, err := f.r.Read(p)
	f.n -= n
	return n, err
}

func importRun(t *testing.T, conn *pgx.Conn, id int64) (linesDone, entries int64, finished bool) {
	t.Helper()
	if err := conn.QueryRow(context.Background(),
		`SELECT lines_done, entries, finished_at IS NOT NULL FROM definition_imports WHERE id = $1`,
		id).Scan(&linesDone, &entries, &finished); err != nil {
		t.Fatal(err)
	}
	return
}

// Повторный импорт того же источника даёт тот же словарь; gzip и
// несжатый JSONL читаются одинаково.
func TestDictionaryImportIdempotent(t *testing.T) {
	a, conn := securityDB(t)
	st := importFixture(t, a, dictionaryFixture, dictionary.Options{Source: "fixture"})
	// 15 строк: 8 статей, 1 испорченная, дубль секции run/verb не пишется
	if st.Lines != 15 || st.Entries != 8 || st.Malformed != 1 || st.Removed != 0 || st.Resumed {
		t.Fatalf("первый прогон: %+v", st)
	}
	want := definitionsSnapshot(t, conn)
	if strings.Count(want, "\n") != 8 || !strings.Contains(want, "run run verb 1 [To move swiftly on foot.") {
		t.Fatalf("словарь после импорта:\n%s", want)
	}

	again := importFixture(t, a, dictionaryFixture, dictionary.Options{Source: "fixture"})
	if again.ImportID == st.ImportID || again.Entries != 8 || again.Removed != 0 {
		t.Fatalf("повторный прогон: %+v", again)
	}
	if got := definitionsSnapshot(t, conn); got != want {
		t.Fatalf("повторный импорт изменил словарь:\n%s\nбыло:\n%s", got, want)
	}

	raw, err := os.ReadFile(dictionaryFixture)
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.Import(context.Background(), a.store.Pool(), &gz,
		dictionary.Options{Source: "fixture.jsonl.gz"}); err != nil {
		t.Fatal(err)
	}
	if got := definitionsSnapshot(t, conn); got != want {
		t.Fatalf("gzip-источник дал другой словарь:\n%s", got)
	}
}

// Оборванный прогон продолжается с контрольной точки и приходит к тому же
// словарю, что и чистый; продолжить другую версию источника нельзя.
func TestDictionaryImportResumes(t *testing.T) {
	clean, cleanConn := securityDB(t)
	importFixture(t, clean, dictionaryFixture, dictionary.Options{Source: "fixture"})
	want := definitionsSnapshot(t, cleanConn)

	a, conn := securityDB(t)
	raw, err := os.ReadFile(dictionaryFixture)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Обрыв посреди одиннадцатой строки: зафиксированы две пачки по 4 строки
	cut := bytes.Index(raw, []byte(`"A duplicate section`))
	opt := dictionary.Options{Source: "dump", SourceVersion: "v1", BatchLines: 4}
	st, err := dictionary.Import(ctx, a.store.Pool(), &failingReader{r: bytes.NewReader(raw), n: cut}, opt)
	if !errors.Is(err, errCut) {
		t.Fatalf("обрыв: %v", err)
	}
	if done, _, finished := importRun(t, conn, st.ImportID); done != 8 || finished {
		t.Fatalf("контрольная точка %d (завершён %v), ждали 8 строк", done, finished)
	}

	_, err = dictionary.Import(ctx, a.store.Pool(), bytes.NewReader(raw),
		dictionary.Options{Source: "dump", SourceVersion: "v2", BatchLines: 4})
	if err == nil || !strings.Contains(err.Error(), "-restart") {
		t.Fatalf("продолжение другой версии источника: %v", err)
	}

	resumed, err := dictionary.Import(ctx, a.store.Pool(), bytes.NewReader(raw), opt)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Resumed || resumed.ImportID != st.ImportID || resumed.Skipped != 8 || resumed.Lines != 15 {
		t.Fatalf("продолжение: %+v", resumed)
	}
	if done, entries, finished := importRun(t, conn, st.ImportID); done != 15 || entries != 8 || !finished {
		t.Fatalf("прогон после продолжения: %d строк, %d статей, завершён %v", done, entries, finished)
	}
	if got := definitionsSnapshot(t, conn); got != want {
		t.Fatalf("продолженный импорт разошёлся с чистым:\n%s\nчистый:\n%s", got, want)
	}

	// -restart с другой версией: v2 — v1 без первой строки, поэтому
	// у run/verb первой статьёй становится дубль секции. Оборванный
	// прогон v1 успел записать свою run/verb, но рестарт приходит
	// к словарю чистого импорта v2
	st, err = dictionary.Import(ctx, a.store.Pool(), &failingReader{r: bytes.NewReader(raw), n: cut}, opt)
	if !errors.Is(err, errCut) {
		t.Fatalf("второй обрыв: %v", err)
	}
	v2 := raw[bytes.IndexByte(raw, '\n')+1:]
	fresh, freshConn := securityDB(t)
	if _, err := dictionary.Import(ctx, fresh.store.Pool(), bytes.NewReader(v2), dictionary.Options{Source: "dump"}); err != nil {
		t.Fatal(err)
	}
	wantV2 := definitionsSnapshot(t, freshConn)
	if !strings.Contains(wantV2, "run run verb 1 [A duplicate section that must not win.]") || wantV2 == want {
		t.Fatalf("v2 не меняет run/verb — тест ничего не проверит:\n%s", wantV2)
	}
	restarted, err := dictionary.Import(ctx, a.store.Pool(), bytes.NewReader(v2),
		dictionary.Options{Source: "dump", SourceVersion: "v2", BatchLines: 4, Restart: true})
	if err != nil || restarted.Resumed || restarted.Skipped != 0 || restarted.ImportID == st.ImportID {
		t.Fatalf("рестарт: %+v, %v", restarted, err)
	}
	if got := definitionsSnapshot(t, conn); got != wantV2 {
		t.Fatalf("рестарт разошёлся с чистым импортом v2:\n%s\nчистый:\n%s", got, wantV2)
	}
	var abandoned, finished bool
	if err := conn.QueryRow(ctx, `SELECT abandoned_at IS NOT NULL, finished_at IS NOT NULL FROM definition_imports WHERE id = $1`,
		st.ImportID).Scan(&abandoned, &finished); err != nil || !abandoned || finished {
		t.Fatalf("брошенный прогон: abandoned %v, finished %v (%v)", abandoned, finished, err)
	}
	if _, _, finished := importRun(t, conn, restarted.ImportID); !finished {
		t.Fatal("прогон после рестарта не завершён")
	}
}

// Завершённый прогон удаляет статьи, пропавшие из источника; источник
// без статей и недокачанный файл прогон не завершают и словарь не трогают.
func TestDictionaryImportRemovesStaleOnlyOnSuccess(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	importFixture(t, a, dictionaryFixture, dictionary.Options{Source: "fixture"})
	full := definitionsSnapshot(t, conn)
	raw, err := os.ReadFile(dictionaryFixture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")

	empty := strings.Join(lines[3:4], "") // только французская статья
	if _, err := dictionary.Import(ctx, a.store.Pool(), strings.NewReader(empty),
		dictionary.Options{Source: "empty"}); !errors.Is(err, dictionary.ErrEmpty) {
		t.Fatalf("пустой источник: %v", err)
	}
	truncated := strings.Join(lines[:6], "") + `{"word": "bank", "lang_code": "en"`
	if _, err := dictionary.Import(ctx, a.store.Pool(), strings.NewReader(truncated),
		dictionary.Options{Source: "truncated"}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("недокачанный источник: %v", err)
	}
	if got := definitionsSnapshot(t, conn); got != full {
		t.Fatalf("незавершённые прогоны изменили словарь:\n%s", got)
	}

	// Новый дамп без bank и building
	reduced := strings.Join(lines[:5], "")
	st, err := dictionary.Import(ctx, a.store.Pool(), strings.NewReader(reduced), dictionary.Options{Source: "reduced"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Entries != 3 || st.Removed != 5 {
		t.Fatalf("сокращённый дамп: %+v", st)
	}
	var words string
	if err := conn.QueryRow(ctx, `SELECT string_agg(word || '/' || pos, ' ' ORDER BY source_line) FROM definitions`).
		Scan(&words); err != nil || words != "run/verb run/noun the/article" {
		t.Fatalf("после сокращённого дампа: %q, %v", words, err)
	}
}

// Второй импорт, пока идёт первый, не начинается.
func TestDictionaryImportIsExclusive(t *testing.T) {
	a, _ := securityDB(t)
	ctx := context.Background()
	holder, err := a.store.Pool().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	// тот же ключ, что берёт Import
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(0x5333_3464_6963_7431)); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, int64(0x5333_3464_6963_7431)) }()
	f, err := os.Open(dictionaryFixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := dictionary.Import(ctx, a.store.Pool(), f, dictionary.Options{Source: "fixture"}); !errors.Is(err, dictionary.ErrBusy) {
		t.Fatalf("параллельный импорт: %v", err)
	}
}

// Миграция 0009 совпадает с моделью данных (terms: lang, lemma, surface,
// translation_cache) и обратима: down убирает таблицы, повторный up
// возвращает их с индексами поиска по лемме.
func TestDictionaryMigrationReverts(t *testing.T) {
	_, conn := securityDB(t)
	ctx := context.Background()
	state := func() string {
		var s string
		if err := conn.QueryRow(ctx, `SELECT coalesce(string_agg(table_name || ':' || cols, ' ' ORDER BY table_name), '')
			FROM (SELECT table_name, string_agg(column_name, ',' ORDER BY column_name) AS cols
			      FROM information_schema.columns
			      WHERE table_schema = current_schema()
			        AND table_name IN ('terms', 'definitions', 'definition_imports')
			      GROUP BY table_name) t`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		var idx string
		if err := conn.QueryRow(ctx, `SELECT coalesce(string_agg(indexname, ',' ORDER BY indexname), '')
			FROM pg_indexes WHERE schemaname = current_schema()
			  AND indexname IN ('terms_lang_lemma_key', 'definitions_lemma_idx', 'definitions_entry_key')`).Scan(&idx); err != nil {
			t.Fatal(err)
		}
		return s + " " + idx
	}
	want := "definition_imports:abandoned_at,created_at,entries,finished_at,id,lines_done,source,source_version " +
		"definitions:etymology,glosses,id,import_id,lemma,pos,source_line,translations_ru,word " +
		"terms:created_at,id,lang,lemma,surface,translation_cache " +
		"definitions_entry_key,definitions_lemma_idx,terms_lang_lemma_key"
	if got := state(); got != want {
		t.Fatalf("схема 0009:\n%s\nждали:\n%s", got, want)
	}
	apply := func(file string) {
		t.Helper()
		sql, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	// Откат идёт в обратном порядке: user_terms (0015) и словарь урока
	// (0019) ссылаются на terms и уходят раньше 0009, а возвращаются после
	apply("../../migrations/0019_lesson_terms.down.sql")
	apply("../../migrations/0015_user_terms.down.sql")
	apply("../../migrations/0009_dictionary.down.sql")
	if got := state(); got != " " {
		t.Fatalf("down оставил %q", got)
	}
	apply("../../migrations/0009_dictionary.up.sql")
	apply("../../migrations/0015_user_terms.up.sql")
	apply("../../migrations/0019_lesson_terms.up.sql")
	if got := state(); got != want {
		t.Fatalf("повторный up: %s", got)
	}
}
