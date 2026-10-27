package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/tatoeba"
	"github.com/IEZhu/class/backend/internal/testdb"
)

// Импорт корпуса Tatoeba (S5-4) и примеры в клике проверяются здесь, как
// и импорт словаря: база у тестов одна — изолированная схема securityDB
// с настоящими миграциями.

const tatoebaFixtures = "../tatoeba/testdata/"

// tatoebaFiles — содержимое трёх файлов фикстуры.
type tatoebaFiles struct{ links, english, russian []byte }

func readTatoebaFixture(t *testing.T) tatoebaFiles {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(tatoebaFixtures + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return tatoebaFiles{
		links:   read("eng-rus_links.tsv"),
		english: read("eng_sentences_detailed.tsv"),
		russian: read("rus_sentences_detailed.tsv"),
	}
}

func (f tatoebaFiles) sources(version string) tatoeba.Sources {
	return tatoeba.Sources{Links: bytes.NewReader(f.links), English: bytes.NewReader(f.english),
		Russian: bytes.NewReader(f.russian), Version: version}
}

func importTatoeba(t *testing.T, a *API, src tatoeba.Sources, opt tatoeba.Options) tatoeba.Stats {
	t.Helper()
	st, err := tatoeba.Import(context.Background(), a.store.Pool(), src, opt)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// tatoebaSnapshot — таблицы корпуса без служебного import_id.
func tatoebaSnapshot(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	return queryDump(t, conn,
		`SELECT id, lang, text, author, lemmas FROM tatoeba_sentences ORDER BY id`,
		`SELECT en_id, ru_id FROM tatoeba_links ORDER BY en_id, ru_id`)
}

// tatoebaVisible — корпус, каким его видят запросы: пары с обоими
// предложениями. Незавершённый прогон оставляет в таблице русские
// предложения без пары — до завершения, которое их уберёт.
func tatoebaVisible(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	return queryDump(t, conn, `SELECT e.id, e.text, e.author, e.lemmas, r.id, r.text, r.author
		FROM tatoeba_links l
		JOIN tatoeba_sentences e ON e.id = l.en_id
		JOIN tatoeba_sentences r ON r.id = l.ru_id
		ORDER BY e.id, r.id`)
}

func queryDump(t *testing.T, conn *pgx.Conn, queries ...string) string {
	t.Helper()
	var b strings.Builder
	for _, q := range queries {
		rows, err := conn.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
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
	}
	return b.String()
}

func tatoebaRun(t *testing.T, conn *pgx.Conn, id int64) (phase string, linesDone int64, finished bool) {
	t.Helper()
	if err := conn.QueryRow(context.Background(),
		`SELECT phase, lines_done, finished_at IS NOT NULL FROM tatoeba_imports WHERE id = $1`,
		id).Scan(&phase, &linesDone, &finished); err != nil {
		t.Fatal(err)
	}
	return
}

// Фикстура: из 12 английских предложений с парами остаются 6 (у прочих
// нет перевода, перевод длиннее потолка или его нет в выгрузке, нет
// слов, строка испорчена); у предложения с двумя переводами — младший.
// Повторный импорт даёт тот же корпус.
func TestTatoebaImportIdempotent(t *testing.T) {
	a, conn := securityDB(t)
	files := readTatoebaFixture(t)
	st := importTatoeba(t, a, files.sources("v1"), tatoeba.Options{})
	// 14 строк пар: 11 разных пар, 2 испорченных; русских: 13 строк,
	// 2 испорченных, 9 в парах и не длиннее потолка; английских: 13 строк,
	// 3 испорченных
	if st.Pairs != 11 || st.Malformed != 7 || st.Russian != 9 || st.English != 6 || st.Links != 6 ||
		st.RemovedSentences != 3 || st.RemovedLinks != 0 || st.Resumed ||
		st.TotalEnglish != 6 || st.TotalRussian != 6 || st.TotalLinks != 6 {
		t.Fatalf("первый прогон: %+v", st)
	}
	want := tatoebaSnapshot(t, conn)
	for _, line := range []string{
		"2 en I run every morning. fixture_en [every i morning run]",
		"4 en He ran home.  [he home run]",
		"102 ru Я бегаю каждое утро. fixture_ru <nil>",
		"2 102", "11 110",
	} {
		if !strings.Contains(want, line+"\n") {
			t.Fatalf("в корпусе нет %q:\n%s", line, want)
		}
	}
	for _, gone := range []string{"\n103 ", "\n6 ", "\n7 ", "\n8 ", "\n12 ", "\n109 ", "\n112 "} {
		if strings.Contains(want, gone) {
			t.Fatalf("в корпусе лишнее %q:\n%s", gone, want)
		}
	}

	again := importTatoeba(t, a, files.sources("v1"), tatoeba.Options{})
	if again.ImportID == st.ImportID || again.English != 6 || again.RemovedSentences != 3 || again.RemovedLinks != 0 {
		t.Fatalf("повторный прогон: %+v", again)
	}
	if got := tatoebaSnapshot(t, conn); got != want {
		t.Fatalf("повторный импорт изменил корпус:\n%s\nбыло:\n%s", got, want)
	}

	// Потолок переводов поднят — у предложения 2 обе пары
	two := importTatoeba(t, a, files.sources("v1"), tatoeba.Options{Limits: tatoeba.Limits{MaxTranslations: 2}})
	if two.TotalLinks != 7 || two.TotalRussian != 7 {
		t.Fatalf("два перевода: %+v", two)
	}
}

// Оборванный прогон продолжается с контрольной точки своей фазы и приходит
// к тому же корпусу, что и чистый; продолжить другую версию выгрузки нельзя.
func TestTatoebaImportResumes(t *testing.T) {
	clean, cleanConn := securityDB(t)
	files := readTatoebaFixture(t)
	importTatoeba(t, clean, files.sources("v1"), tatoeba.Options{})
	want := tatoebaSnapshot(t, cleanConn)

	a, conn := securityDB(t)
	ctx := context.Background()
	opt := tatoeba.Options{BatchLines: 4}
	cutAt := func(data []byte, marker string) io.Reader {
		return &failingReader{r: bytes.NewReader(data), n: bytes.Index(data, []byte(marker))}
	}

	// Обрыв русских посреди шестой строки: зафиксирована одна пачка
	src := files.sources("v1")
	src.Russian = cutAt(files.russian, "Банк")
	st, err := tatoeba.Import(ctx, a.store.Pool(), src, opt)
	if !errors.Is(err, errCut) {
		t.Fatalf("обрыв русских: %v", err)
	}
	if phase, done, finished := tatoebaRun(t, conn, st.ImportID); phase != "ru" || done != 4 || finished {
		t.Fatalf("контрольная точка %s/%d (завершён %v), ждали ru/4", phase, done, finished)
	}

	if _, err := tatoeba.Import(ctx, a.store.Pool(), files.sources("v2"), opt); err == nil ||
		!strings.Contains(err.Error(), "-restart") {
		t.Fatalf("продолжение другой версии выгрузки: %v", err)
	}
	// Потолки — часть версии: с другими продолжать тоже нельзя
	if _, err := tatoeba.Import(ctx, a.store.Pool(), files.sources("v1"),
		tatoeba.Options{BatchLines: 4, Limits: tatoeba.Limits{MaxRunes: 80}}); err == nil {
		t.Fatal("продолжение с другим потолком длины")
	}

	// Продолжение обрывается на английских посреди седьмой строки
	src = files.sources("v1")
	src.English = cutAt(files.english, "2 + 2")
	resumed, err := tatoeba.Import(ctx, a.store.Pool(), src, opt)
	if !errors.Is(err, errCut) || !resumed.Resumed || resumed.Phase != "ru" || resumed.Skipped != 4 ||
		resumed.ImportID != st.ImportID {
		t.Fatalf("продолжение русских: %+v, %v", resumed, err)
	}
	if phase, done, finished := tatoebaRun(t, conn, st.ImportID); phase != "en" || done != 4 || finished {
		t.Fatalf("контрольная точка %s/%d (завершён %v), ждали en/4", phase, done, finished)
	}

	// Русские в английской фазе не читаются: годится и пустой поток
	src = files.sources("v1")
	src.Russian = strings.NewReader("")
	final, err := tatoeba.Import(ctx, a.store.Pool(), src, opt)
	if err != nil || !final.Resumed || final.Phase != "en" || final.Skipped != 4 || final.Russian != 0 {
		t.Fatalf("продолжение английских: %+v, %v", final, err)
	}
	if _, _, finished := tatoebaRun(t, conn, st.ImportID); !finished {
		t.Fatal("прогон не завершён")
	}
	if got := tatoebaSnapshot(t, conn); got != want {
		t.Fatalf("продолженный импорт разошёлся с чистым:\n%s\nчистый:\n%s", got, want)
	}

	// -restart: оборванный прогон новой версии начинается с первой фазы
	src = files.sources("v2")
	src.English = cutAt(files.english, "2 + 2")
	cut, err := tatoeba.Import(ctx, a.store.Pool(), src, opt)
	if !errors.Is(err, errCut) {
		t.Fatalf("второй обрыв: %v", err)
	}
	restarted, err := tatoeba.Import(ctx, a.store.Pool(), files.sources("v3"),
		tatoeba.Options{BatchLines: 4, Restart: true})
	if err != nil || restarted.Resumed || restarted.Phase != "ru" || restarted.Russian != 9 ||
		restarted.ImportID == cut.ImportID {
		t.Fatalf("рестарт: %+v, %v", restarted, err)
	}
	if got := tatoebaSnapshot(t, conn); got != want {
		t.Fatalf("рестарт разошёлся с чистым импортом:\n%s", got)
	}

	// -restart с меньшим потолком: пары и предложения, которые брошенный
	// прогон записал по прежнему потолку, уходят при завершении нового
	// («Он побежал домой.» — 17 символов при потолке 15, и «He ran home.»
	// остаётся без перевода)
	short := tatoeba.Options{BatchLines: 4, Limits: tatoeba.Limits{MaxRunes: 15}}
	importTatoeba(t, clean, files.sources("v4"), short)
	wantShort := tatoebaSnapshot(t, cleanConn)
	src = files.sources("v4")
	src.English = cutAt(files.english, "2 + 2")
	if _, err := tatoeba.Import(ctx, a.store.Pool(), src, opt); !errors.Is(err, errCut) {
		t.Fatalf("третий обрыв: %v", err)
	}
	short.Restart = true
	importTatoeba(t, a, files.sources("v4"), short)
	if got := tatoebaSnapshot(t, conn); got != wantShort {
		t.Fatalf("рестарт с потолком 15 разошёлся с чистым импортом:\n%s\nчистый:\n%s", got, wantShort)
	}
}

// Завершённый прогон удаляет то, что пропало из выгрузки; выгрузка без
// английских предложений и недокачанный файл прогон не завершают и
// видимый корпус не меняют.
func TestTatoebaImportRemovesStaleOnlyOnSuccess(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	files := readTatoebaFixture(t)
	importTatoeba(t, a, files.sources("v1"), tatoeba.Options{})
	full := tatoebaVisible(t, conn)

	empty := files.sources("empty")
	empty.English = strings.NewReader("6\teng\tThis sentence has no Russian translation.\tfixture_en\t\\N\t\\N\n")
	if _, err := tatoeba.Import(ctx, a.store.Pool(), empty, tatoeba.Options{}); !errors.Is(err, tatoeba.ErrEmpty) {
		t.Fatalf("выгрузка без английских с переводом: %v", err)
	}
	truncated := files.sources("truncated")
	truncated.English = bytes.NewReader(files.english[:bytes.Index(files.english, []byte("The bank"))])
	if _, err := tatoeba.Import(ctx, a.store.Pool(), truncated, tatoeba.Options{Restart: true}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("недокачанная выгрузка: %v", err)
	}
	if got := tatoebaVisible(t, conn); got != full {
		t.Fatalf("незавершённые прогоны изменили корпус:\n%s\nбыло:\n%s", got, full)
	}

	// Новая выгрузка без предложения про банк: уходят оно, его пара и перевод
	var reduced []string
	for _, line := range strings.SplitAfter(string(files.english), "\n") {
		if !strings.Contains(line, "bank") {
			reduced = append(reduced, line)
		}
	}
	src := files.sources("reduced")
	src.English = strings.NewReader(strings.Join(reduced, ""))
	st, err := tatoeba.Import(ctx, a.store.Pool(), src, tatoeba.Options{Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	// Удалены: английское 5 (прошлый импорт) и русские без пары — 106 и
	// снова записанные русской фазой 103, 107, 108
	if st.English != 5 || st.RemovedLinks != 1 || st.RemovedSentences != 5 ||
		st.TotalEnglish != 5 || st.TotalRussian != 5 || st.TotalLinks != 5 {
		t.Fatalf("сокращённая выгрузка: %+v", st)
	}
	var bank int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM tatoeba_sentences WHERE id IN (5, 106)`).Scan(&bank); err != nil || bank != 0 {
		t.Fatalf("предложение и перевод про банк остались: %d, %v", bank, err)
	}
}

// Второй импорт, пока идёт первый, не начинается.
func TestTatoebaImportIsExclusive(t *testing.T) {
	a, _ := securityDB(t)
	ctx := context.Background()
	holder, err := a.store.Pool().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	const lock = int64(0x5335_3474_6174_6f65) // тот же ключ, что берёт Import
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, lock); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lock) }()
	if _, err := tatoeba.Import(ctx, a.store.Pool(), readTatoebaFixture(t).sources("v1"),
		tatoeba.Options{}); !errors.Is(err, tatoeba.ErrBusy) {
		t.Fatalf("параллельный импорт: %v", err)
	}
}

// Миграция 0016 обратима: down убирает таблицы корпуса, повторный up
// возвращает их с индексами.
func TestTatoebaMigrationReverts(t *testing.T) {
	_, conn := securityDB(t)
	ctx := context.Background()
	state := func() string {
		var s string
		if err := conn.QueryRow(ctx, `SELECT coalesce(string_agg(table_name || ':' || cols, ' ' ORDER BY table_name), '')
			FROM (SELECT table_name, string_agg(column_name, ',' ORDER BY column_name) AS cols
			      FROM information_schema.columns
			      WHERE table_schema = current_schema()
			        AND table_name IN ('tatoeba_imports', 'tatoeba_sentences', 'tatoeba_links')
			      GROUP BY table_name) t`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		var idx string
		if err := conn.QueryRow(ctx, `SELECT coalesce(string_agg(indexname, ',' ORDER BY indexname), '')
			FROM pg_indexes WHERE schemaname = current_schema() AND indexname LIKE 'tatoeba%'`).Scan(&idx); err != nil {
			t.Fatal(err)
		}
		return s + " " + idx
	}
	want := "tatoeba_imports:abandoned_at,created_at,finished_at,id,lines_done,phase,source_version " +
		"tatoeba_links:en_id,import_id,ru_id " +
		"tatoeba_sentences:author,id,import_id,lang,lemmas,text " +
		"tatoeba_imports_pkey,tatoeba_imports_running_key,tatoeba_links_pkey,tatoeba_links_ru_idx," +
		"tatoeba_sentences_lemmas_idx,tatoeba_sentences_pkey"
	if got := state(); got != want {
		t.Fatalf("схема 0016:\n%s\nждали:\n%s", got, want)
	}
	testdb.Apply(t, conn, "0016_tatoeba.down.sql")
	if got := state(); got != " " {
		t.Fatalf("down оставил %q", got)
	}
	testdb.Apply(t, conn, "0016_tatoeba.up.sql")
	if got := state(); got != want {
		t.Fatalf("повторный up: %s", got)
	}
}

// noNetwork роняет тест на любом исходящем HTTP-запросе через
// http.DefaultTransport.
type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Errorf("внешний запрос из клика: %s %s", r.Method, r.URL)
	return nil, errors.New("сеть запрещена в тесте")
}

func clickExamples(t *testing.T, h http.Handler, cookie *http.Cookie, surface string) []termExample {
	t.Helper()
	w := securityRequest(h, "POST", "/terms/click", `{"surface":"`+surface+`"}`, cookie)
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"examples":[`) {
		t.Fatalf("examples — список, не null: %s", w.Body.String())
	}
	var resp termClickResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Examples
}

// DoD S5-4 (закрывает D-1): у слова из корпуса в попапе 1–2 коротких
// примера с переводом и подписью CC BY 2.0 FR, в любой форме слова; без
// корпуса — пустой список. Клик за примерами в сеть не ходит.
func TestTermClickExamples(t *testing.T) {
	prev := http.DefaultTransport
	http.DefaultTransport = noNetwork{t}
	t.Cleanup(func() { http.DefaultTransport = prev })

	a, conn := securityDB(t)
	_, student := securityUser(t, a, conn, store.RoleStudent, "reader")
	h := a.Router()
	// Корпус ещё не импортирован
	if got := clickExamples(t, h, student, "run"); len(got) != 0 {
		t.Fatalf("без корпуса: %+v", got)
	}

	importTatoeba(t, a, readTatoebaFixture(t).sources("v1"), tatoeba.Options{})
	// Предложения с run: «Run!» (одно слово — последним), «He ran home.»
	// (12 символов), «I run every morning.» и «We are running late.»
	// (по 20, младший id первым), длинное про станцию
	want := []termExample{
		{Text: "He ran home.", Translation: "Он побежал домой.", Source: "tatoeba",
			Attribution: exampleAttribution{
				Author: "", URL: "https://tatoeba.org/sentences/show/4",
				TranslationAuthor: "", TranslationURL: "https://tatoeba.org/sentences/show/105",
				License: "CC BY 2.0 FR", LicenseURL: "https://creativecommons.org/licenses/by/2.0/fr/",
			}},
		{Text: "I run every morning.", Translation: "Я бегаю каждое утро.", Source: "tatoeba",
			Attribution: exampleAttribution{
				Author: "fixture_en", URL: "https://tatoeba.org/sentences/show/2",
				TranslationAuthor: "fixture_ru", TranslationURL: "https://tatoeba.org/sentences/show/102",
				License: "CC BY 2.0 FR", LicenseURL: "https://creativecommons.org/licenses/by/2.0/fr/",
			}},
	}
	for _, surface := range []string{"running", "ran", "Run"} {
		got := clickExamples(t, h, student, surface)
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("%s: %+v", surface, got)
		}
	}
	if got := clickExamples(t, h, student, "banks"); len(got) != 1 || got[0].Text != "The bank is closed today." ||
		got[0].Translation != "Банк сегодня закрыт." || got[0].Attribution.Author != "fixture_bank" {
		t.Fatalf("bank: %+v", got)
	}
	// Слова нет в корпусе; у building перевод не попал в выгрузку
	for _, surface := range []string{"zzyzxq", "building", "translation"} {
		if got := clickExamples(t, h, student, surface); len(got) != 0 {
			t.Fatalf("%s: %+v", surface, got)
		}
	}

	// Миграция откатилась (или ещё не применена): клик работает без примеров
	testdb.Apply(t, conn, "0016_tatoeba.down.sql")
	if got := clickExamples(t, h, student, "run"); len(got) != 0 {
		t.Fatalf("без таблиц корпуса: %+v", got)
	}
}
