package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/llm/llmtest"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/tatoeba"
	"github.com/IEZhu/class/backend/internal/testdb"
)

// practiceRunText — текст «LLM», где run, morning и bank встречаются
// дважды: проходит валидатор генерации этих трёх слов.
const practiceRunText = "Every morning I run to the bank. The bank opens late, so every morning I run home."

func searchTexts(t *testing.T, h http.Handler, cookie *http.Cookie, query string) practiceTextsPage {
	t.Helper()
	w := securityRequest(h, "GET", "/practice/texts/search"+query, "", cookie)
	expectStatus(t, w, http.StatusOK)
	var page practiceTextsPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

// summary — выдача строками «источник:покрытие:текст» для сравнения.
func summary(items []practiceTextResponse) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, fmt.Sprintf("%s:%s:%s", it.Source, strings.Join(it.Matched, ","), it.Text))
	}
	return out
}

func saveWord(t *testing.T, a *API, userID int64, word string, status int) {
	t.Helper()
	term, err := a.store.EnsureTerm(context.Background(), termLang, word, word)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.SaveUserTerm(context.Background(), userID, term.ID, &status, nil); err != nil {
		t.Fatal(err)
	}
}

// practiceSearchStand — корпус Tatoeba из фикстуры, словарь студента
// (run — новое, morning, bank; late — «знаю»), его текст LLM, текст
// до миграции 0020 (без lemmas) и чужой текст с теми же словами.
func practiceSearchStand(t *testing.T) (*API, *pgx.Conn, http.Handler, *http.Cookie) {
	t.Helper()
	a, conn := practiceAPI(t, llmtest.Texts(practiceRunText), 0)
	ctx := context.Background()
	studentID, student := securityUser(t, a, conn, store.RoleStudent, "texts-student")
	_, other := securityUser(t, a, conn, store.RoleStudent, "texts-other")
	importTatoeba(t, a, readTatoebaFixture(t).sources("v1"), tatoeba.Options{})
	saveWord(t, a, studentID, "run", 1)
	saveWord(t, a, studentID, "morning", 2)
	saveWord(t, a, studentID, "bank", 3)
	saveWord(t, a, studentID, "late", 5)
	h := a.Router()
	body := `{"words":["run","morning","bank"],"level":"A2","max_words":50}`
	for _, cookie := range []*http.Cookie{other, student} {
		expectStatus(t, securityRequest(h, "POST", "/practice/generate-text", body, cookie), http.StatusCreated)
	}
	// Генерация записала леммы всего текста, а не только целей
	if n := countRows(t, conn, `SELECT count(*) FROM generated_texts
		WHERE lemmas @> ARRAY['run', 'morning', 'bank', 'home', 'open']`); n != 2 {
		t.Fatalf("текстов с леммами всего текста: %d", n)
	}
	// Текст до 0020: lemmas NULL, покрытие — по целям (run в тексте есть,
	// но в цели не входил)
	if _, err := conn.Exec(ctx, `INSERT INTO generated_texts (user_id, level, genre, max_words,
		    target_lemmas, body, word_count, coverage, occurrences, unknown_share, attempts, provider, model)
		VALUES ($1, 'B1', 'story', 50, ARRAY['bank'], 'The bank was closed, so we ran to the other bank.',
		    11, 1, '{"bank": 2}', 0, 1, 'fake', 'fake-model')`, studentID); err != nil {
		t.Fatal(err)
	}
	return a, conn, h, student
}

// Поиск: свои тексты и предложения Tatoeba с переводом по невыученным
// словам словаря; ранжирование — по числу слов, при равенстве свои
// тексты первыми, затем короткие предложения. «Знаю» (late) не в счёт,
// чужой текст не виден, предложение без перевода и из одного слова
// («Run!») не попадают.
func TestPracticeTextSearchRanksOwnTextsAndTatoeba(t *testing.T) {
	_, _, h, student := practiceSearchStand(t)
	page := searchTexts(t, h, student, "")
	if !reflect.DeepEqual(page.Words, []string{"run", "morning", "bank"}) || page.Hint != nil || page.NextCursor != nil {
		t.Fatalf("слова %v, подсказка %v, курсор %v", page.Words, page.Hint, page.NextCursor)
	}
	want := []string{
		"generated:run,morning,bank:" + practiceRunText,
		"tatoeba:run,morning:I run every morning.",
		"generated:bank:The bank was closed, so we ran to the other bank.",
		"tatoeba:run:He ran home.",
		"tatoeba:run:We are running late.",
		"tatoeba:bank:The bank is closed today.",
		"tatoeba:run:She was running to the station because the last train was about to leave.",
	}
	if got := summary(page.Items); !reflect.DeepEqual(got, want) {
		t.Fatalf("выдача:\n%s\nхотели:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	own := page.Items[0]
	if own.Level != "A2" || own.Genre != "story" || own.WordCount == 0 || own.CreatedAt == nil ||
		own.Translation != "" || own.Attribution != nil {
		t.Fatalf("свой текст: %+v", own)
	}
	sentence := page.Items[1]
	wantAttr := exampleAttribution{
		Author: "fixture_en", URL: "https://tatoeba.org/sentences/show/2",
		TranslationAuthor: "fixture_ru", TranslationURL: "https://tatoeba.org/sentences/show/102",
		License: "CC BY 2.0 FR", LicenseURL: "https://creativecommons.org/licenses/by/2.0/fr/",
	}
	if sentence.Translation != "Я бегаю каждое утро." || sentence.Attribution == nil || *sentence.Attribution != wantAttr ||
		sentence.Level != "" || sentence.CreatedAt != nil {
		t.Fatalf("предложение: %+v", sentence)
	}

	// Явный список слов вместо словаря: формы сводятся к леммам, повторы
	// убираются, порядок — как в запросе
	page = searchTexts(t, h, student, "?words="+url.QueryEscape("Banks, closed bank"))
	if !reflect.DeepEqual(page.Words, []string{"bank", "close"}) {
		t.Fatalf("слова из запроса: %v", page.Words)
	}
	// У текста до 0020 в счёт только цели: closed в нём есть, но целью не был
	want = []string{
		"tatoeba:bank,close:The bank is closed today.",
		"generated:bank:The bank was closed, so we ran to the other bank.",
		"generated:bank:" + practiceRunText,
	}
	if got := summary(page.Items); !reflect.DeepEqual(got, want) {
		t.Fatalf("по словам запроса:\n%s", strings.Join(got, "\n"))
	}
}

// Курсор: страницы по одной-две строки вместе дают ту же выдачу, что
// одна большая, без повторов и пропусков; последняя — без курсора.
func TestPracticeTextSearchPaginates(t *testing.T) {
	_, _, h, student := practiceSearchStand(t)
	all := summary(searchTexts(t, h, student, "?limit=50").Items)
	if len(all) != 7 {
		t.Fatalf("вся выдача: %v", all)
	}
	for _, limit := range []int{1, 2, 3} {
		var got []string
		cursor := ""
		for pages := 0; ; pages++ {
			if pages > len(all) {
				t.Fatalf("limit %d: курсор не кончается", limit)
			}
			q := fmt.Sprintf("?limit=%d", limit)
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			page := searchTexts(t, h, student, q)
			if len(page.Items) > limit {
				t.Fatalf("limit %d: страница из %d", limit, len(page.Items))
			}
			got = append(got, summary(page.Items)...)
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		if !reflect.DeepEqual(got, all) {
			t.Fatalf("limit %d: по страницам\n%s\nцеликом\n%s", limit, strings.Join(got, "\n"), strings.Join(all, "\n"))
		}
	}
	many := make([]string, maxSearchWords+1)
	for i := range many {
		many[i] = "word" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	for _, q := range []string{"?limit=0", "?limit=51", "?limit=x", "?cursor=1_2_3", "?cursor=0_1_20_2",
		"?cursor=1_2_20_2", "?cursor=1_1_20_0", "?cursor=a_1_20_2", "?words=c%40t", "?words=" + strings.Join(many, ",")} {
		expectStatus(t, securityRequest(h, "GET", "/practice/texts/search"+q, "", student), http.StatusBadRequest)
	}
}

// Вызывающий без невыученных слов (пустой словарь, одни «знаю»,
// преподаватель без словаря) получает пустую выдачу с подсказкой, а не
// ошибку; со списком слов преподаватель ищет как студент — по Tatoeba
// и своим текстам. Аноним — 401, роли без практики — 403.
func TestPracticeTextSearchWithoutWordsAndAccess(t *testing.T) {
	a, conn, h, _ := practiceSearchStand(t)
	_, fresh := securityUser(t, a, conn, store.RoleStudent, "texts-fresh")
	knowsID, knows := securityUser(t, a, conn, store.RoleStudent, "texts-knows")
	saveWord(t, a, knowsID, "run", 5)
	_, teacher := securityUser(t, a, conn, store.RoleTeacher, "texts-teacher")
	for _, cookie := range []*http.Cookie{fresh, knows, teacher} {
		page := searchTexts(t, h, cookie, "")
		if len(page.Words) != 0 || len(page.Items) != 0 || page.Hint == nil || *page.Hint != noSearchWordsHint ||
			page.NextCursor != nil {
			t.Fatalf("без слов: %+v", page)
		}
	}
	page := searchTexts(t, h, teacher, "?words=bank")
	if got := summary(page.Items); !reflect.DeepEqual(got, []string{"tatoeba:bank:The bank is closed today."}) {
		t.Fatalf("преподаватель по словам: %v", got)
	}

	expectStatus(t, securityRequest(h, "GET", "/practice/texts/search", "", nil), http.StatusUnauthorized)
	_, admin := securityUser(t, a, conn, store.RoleAdmin, "texts-admin")
	expectStatus(t, securityRequest(h, "GET", "/practice/texts/search", "", admin), http.StatusForbidden)
}

// Миграция 0020 обратима: откат убирает леммы текстов, индекс уборки
// сессий и её джобы с расписанием, повторный накат возвращает колонку
// и индекс.
func TestMigration0020Reversible(t *testing.T) {
	_, conn := securityDB(t)
	ctx := context.Background()
	for _, sql := range []string{`INSERT INTO jobs (kind) VALUES ($1)`,
		`INSERT INTO job_schedules (kind, slot) VALUES ($1, now())`} {
		if _, err := conn.Exec(ctx, sql, store.JobSessionCleanup); err != nil {
			t.Fatal(err)
		}
	}
	state := func() string {
		t.Helper()
		var s string
		if err := conn.QueryRow(ctx, `SELECT concat_ws(',',
			    (SELECT 'lemmas' FROM information_schema.columns
			     WHERE table_schema = current_schema() AND table_name = 'generated_texts' AND column_name = 'lemmas'),
			    to_regclass('sessions_expires_idx'),
			    (SELECT 'jobs=' || count(*) FROM jobs WHERE kind = $1),
			    (SELECT 'schedules=' || count(*) FROM job_schedules WHERE kind = $1))`,
			store.JobSessionCleanup).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := state(); got != "lemmas,sessions_expires_idx,jobs=1,schedules=1" {
		t.Fatalf("после наката: %q", got)
	}
	testdb.Apply(t, conn, "0020_text_practice.down.sql")
	if got := state(); got != "jobs=0,schedules=0" {
		t.Fatalf("после отката: %q", got)
	}
	testdb.Apply(t, conn, "0020_text_practice.up.sql")
	if got := state(); got != "lemmas,sessions_expires_idx,jobs=0,schedules=0" {
		t.Fatalf("после повторного наката: %q", got)
	}
}
