package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/lemma"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/testdb"
)

func saveUserTerm(t *testing.T, h http.Handler, cookie *http.Cookie, body string, want int) userTermResponse {
	t.Helper()
	w := securityRequest(h, "POST", "/user-terms", body, cookie)
	expectStatus(t, w, want)
	var resp userTermResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func listUserTerms(t *testing.T, h http.Handler, cookie *http.Cookie, query string) userTermsPage {
	t.Helper()
	w := securityRequest(h, "GET", "/user-terms"+query, "", cookie)
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"items":[`) {
		t.Fatalf("items — список, не null: %s", w.Body.String())
	}
	var page userTermsPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func lookupStatuses(t *testing.T, h http.Handler, cookie *http.Cookie, words []string) lookupResponse {
	t.Helper()
	body, err := json.Marshal(lookupRequest{Words: words})
	if err != nil {
		t.Fatal(err)
	}
	w := securityRequest(h, "POST", "/user-terms/lookup", string(body), cookie)
	expectStatus(t, w, http.StatusOK)
	var resp lookupResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func ensureTerm(t *testing.T, a *API, word string) int64 {
	t.Helper()
	term, err := a.store.EnsureTerm(context.Background(), termLang, lemma.Lemma(word), word)
	if err != nil {
		t.Fatal(err)
	}
	return term.ID
}

// lessonWithStudent — урок группы, в которой состоит студент (снапшот
// участников при создании), и преподаватель урока.
func lessonWithStudent(t *testing.T, a *API, conn *pgx.Conn, teacherID int64, group string, studentIDs ...int64) int64 {
	t.Helper()
	ctx := context.Background()
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name, level) VALUES ($1, 'B1') RETURNING id`, group).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	for _, id := range studentIDs {
		if _, err := conn.Exec(ctx, `INSERT INTO group_members(group_id, user_id) VALUES ($1, $2)`, groupID, id); err != nil {
			t.Fatal(err)
		}
	}
	lesson, err := a.store.CreateLesson(ctx, &groupID, "", teacherID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return lesson.ID
}

// DoD S3-5: слово из попапа ложится в словарь, статус меняется, слово
// убирается; клик показывает статус; список фильтруется по статусу.
func TestUserTermsAddChangeDelete(t *testing.T) {
	lt := newFakeLibreTranslate(t, map[string]string{"run": "бежать"})
	a, _, student := termsAPI(t, lt.srv.URL)
	h := a.Router()

	clicked := click(t, h, student, `{"surface":"Running","context_sentence":"He is running home."}`)
	if clicked.Status != nil {
		t.Fatalf("слова ещё нет в словаре, а статус %d", *clicked.Status)
	}
	body := fmt.Sprintf(`{"term_id":%d}`, clicked.TermID)
	added := saveUserTerm(t, h, student, body, http.StatusCreated)
	if added.Status != 1 || added.Lemma != "run" || added.Surface != "Running" ||
		added.Translation == nil || *added.Translation != "бежать" || added.SourceLesson != nil {
		t.Fatalf("добавление: %+v", added)
	}
	if again := click(t, h, student, `{"surface":"ran"}`); again.Status == nil || *again.Status != 1 {
		t.Fatalf("клик по другой форме не видит статус: %+v", again.Status)
	}

	changed := saveUserTerm(t, h, student, fmt.Sprintf(`{"term_id":%d,"status":3}`, clicked.TermID), http.StatusOK)
	if changed.Status != 3 || !changed.AddedAt.Equal(added.AddedAt) || changed.UpdatedAt.Before(added.UpdatedAt) {
		t.Fatalf("смена статуса: %+v", changed)
	}
	// Тот же статус и запрос без статуса ничего не меняют, даже updated_at
	for _, b := range []string{fmt.Sprintf(`{"term_id":%d,"status":3}`, clicked.TermID), body} {
		same := saveUserTerm(t, h, student, b, http.StatusOK)
		if same.Status != 3 || !same.UpdatedAt.Equal(changed.UpdatedAt) {
			t.Fatalf("повтор %s: %+v", b, same)
		}
	}

	for query, want := range map[string]int{
		"": 1, "?status=3": 1, "?status=1,2": 0, "?status=1&status=3": 1, "?status=4,5": 0,
	} {
		page := listUserTerms(t, h, student, query)
		if len(page.Items) != want || page.NextCursor != nil {
			t.Fatalf("GET /user-terms%s: %+v, ждали %d", query, page, want)
		}
		if want == 1 && (page.Items[0].Lemma != "run" || page.Items[0].Status != 3) {
			t.Fatalf("строка словаря: %+v", page.Items[0])
		}
	}

	path := fmt.Sprintf("/user-terms/%d", clicked.TermID)
	expectStatus(t, securityRequest(h, "DELETE", path, "", student), http.StatusNoContent)
	expectStatus(t, securityRequest(h, "DELETE", path, "", student), http.StatusNotFound)
	if page := listUserTerms(t, h, student, ""); len(page.Items) != 0 {
		t.Fatalf("после удаления: %+v", page)
	}
	if again := click(t, h, student, `{"surface":"run"}`); again.Status != nil {
		t.Fatalf("удалённое слово всё ещё со статусом %d", *again.Status)
	}
	// Термина нет — 404, строка не появляется
	saveUserTerm(t, h, student, `{"term_id":987654}`, http.StatusNotFound)
}

func TestUserTermsValidation(t *testing.T) {
	a, conn, student := termsAPI(t, "")
	h := a.Router()
	termID := ensureTerm(t, a, "house")
	for _, body := range []string{
		`{}`,
		`{"term_id":0}`,
		`{"term_id":-1}`,
		fmt.Sprintf(`{"term_id":%d,"status":0}`, termID),
		fmt.Sprintf(`{"term_id":%d,"status":6}`, termID),
		fmt.Sprintf(`{"term_id":%d,"status":-1}`, termID),
		fmt.Sprintf(`{"term_id":%d,"status":"2"}`, termID),
		fmt.Sprintf(`{"term_id":%d,"status":2.5}`, termID),
		fmt.Sprintf(`{"term_id":%d,"source_lesson_id":0}`, termID),
		fmt.Sprintf(`{"term_id":"%d"}`, termID),
		`not json`,
	} {
		if w := securityRequest(h, "POST", "/user-terms", body, student); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, хотели 400; %s", body, w.Code, w.Body.String())
		}
	}
	for _, query := range []string{"?status=0", "?status=6", "?status=x", "?status=1,", "?limit=0", "?limit=501", "?cursor=bad", "?cursor=1_0"} {
		if w := securityRequest(h, "GET", "/user-terms"+query, "", student); w.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, хотели 400", query, w.Code)
		}
	}
	for _, body := range []string{`{"words":"run"}`, `[]`, `not json`, `{"words":["a"]}{}`} {
		if w := securityRequest(h, "POST", "/user-terms/lookup", body, student); w.Code != http.StatusBadRequest {
			t.Errorf("lookup %s: status %d, хотели 400", body, w.Code)
		}
	}
	tooMany, _ := json.Marshal(lookupRequest{Words: slices.Repeat([]string{"run"}, maxLookupWords+1)})
	expectStatus(t, securityRequest(h, "POST", "/user-terms/lookup", string(tooMany), student), http.StatusBadRequest)
	tooBig := `{"words":["` + strings.Repeat("a", maxLookupBytes) + `"]}`
	expectStatus(t, securityRequest(h, "POST", "/user-terms/lookup", tooBig, student), http.StatusRequestEntityTooLarge)

	var n int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM user_terms`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("невалидные запросы записали %d строк: %v", n, err)
	}
	// Диапазон и уникальность держит и сама схема, не только api
	var userID int64
	if err := conn.QueryRow(context.Background(), `SELECT id FROM users WHERE name = 'clicker'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	for status, code := range map[int]string{0: pgCheckViolation, 6: pgCheckViolation} {
		_, err := conn.Exec(context.Background(),
			`INSERT INTO user_terms (user_id, term_id, status) VALUES ($1, $2, $3)`, userID, termID, status)
		if store.PgErrorCode(err) != code {
			t.Fatalf("status %d в обход api: %v", status, err)
		}
	}
	if _, err := conn.Exec(context.Background(),
		`INSERT INTO user_terms (user_id, term_id) VALUES ($1, $2), ($1, $2)`, userID, termID); store.PgErrorCode(err) != pgUniqueViolation {
		t.Fatalf("дубль (user, term) в обход api: %v", err)
	}
}

// Слово у пользователя одно: параллельные «в мой словарь» дают одну
// строку и ровно один 201; у другого студента — своя строка.
func TestUserTermsUniquePerUserAndTerm(t *testing.T) {
	a, conn, student := termsAPI(t, "")
	h := a.Router()
	termID := ensureTerm(t, a, "bank")
	body := fmt.Sprintf(`{"term_id":%d,"status":2}`, termID)

	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for range 8 {
		wg.Go(func() { codes <- securityRequest(h, "POST", "/user-terms", body, student).Code })
	}
	wg.Wait()
	close(codes)
	created := 0
	for code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusOK:
		default:
			t.Fatalf("параллельное добавление: %d", code)
		}
	}
	if created != 1 {
		t.Fatalf("201 получили %d запросов, ждали один", created)
	}

	_, other := securityUser(t, a, conn, store.RoleStudent, "other-student")
	saveUserTerm(t, h, other, fmt.Sprintf(`{"term_id":%d,"status":5}`, termID), http.StatusCreated)
	var rows int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM user_terms WHERE term_id = $1`, termID).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("строк %d, ждали по одной на студента: %v", rows, err)
	}
	if page := listUserTerms(t, h, student, ""); len(page.Items) != 1 || page.Items[0].Status != 2 {
		t.Fatalf("словарь первого студента: %+v", page.Items)
	}
	if page := listUserTerms(t, h, other, ""); len(page.Items) != 1 || page.Items[0].Status != 5 {
		t.Fatalf("словарь второго студента: %+v", page.Items)
	}
}

// source_lesson_id — только урок, который студент видит; источник —
// первый урок, а отмена урока обнуляет его, не трогая слово.
func TestUserTermsSourceLesson(t *testing.T) {
	a, conn, student := termsAPI(t, "")
	h := a.Router()
	ctx := context.Background()
	var studentID int64
	if err := conn.QueryRow(ctx, `SELECT id FROM users WHERE name = 'clicker'`).Scan(&studentID); err != nil {
		t.Fatal(err)
	}
	teacherID, _ := securityUser(t, a, conn, store.RoleTeacher, "words-teacher")
	_, otherTeacher := securityUser(t, a, conn, store.RoleTeacher, "foreign-teacher")
	own := lessonWithStudent(t, a, conn, teacherID, "own-group", studentID)
	second := lessonWithStudent(t, a, conn, teacherID, "second-group", studentID)
	foreign := lessonWithStudent(t, a, conn, teacherID, "foreign-group")
	run, bank := ensureTerm(t, a, "run"), ensureTerm(t, a, "bank")

	saveUserTerm(t, h, student, fmt.Sprintf(`{"term_id":%d,"source_lesson_id":%d}`, run, foreign), http.StatusForbidden)
	saveUserTerm(t, h, student, fmt.Sprintf(`{"term_id":%d,"source_lesson_id":%d}`, run, foreign+1000), http.StatusNotFound)
	if page := listUserTerms(t, h, student, ""); len(page.Items) != 0 {
		t.Fatalf("отказ записал слово: %+v", page.Items)
	}

	added := saveUserTerm(t, h, student, fmt.Sprintf(`{"term_id":%d,"source_lesson_id":%d}`, run, own), http.StatusCreated)
	if l := added.SourceLesson; l == nil || l.ID != own || l.GroupName != "own-group" || l.GroupID == nil || l.StartsAt.IsZero() {
		t.Fatalf("источник: %+v", added.SourceLesson)
	}
	// Смена статуса с другого урока источник не переписывает
	changed := saveUserTerm(t, h, student, fmt.Sprintf(`{"term_id":%d,"status":4,"source_lesson_id":%d}`, run, second), http.StatusOK)
	if changed.Status != 4 || changed.SourceLesson == nil || changed.SourceLesson.ID != own {
		t.Fatalf("источник переписан: %+v", changed)
	}
	// Добавленное вне урока слово получает источник со следующего урока
	saveUserTerm(t, h, student, fmt.Sprintf(`{"term_id":%d}`, bank), http.StatusCreated)
	filled := saveUserTerm(t, h, student, fmt.Sprintf(`{"term_id":%d,"source_lesson_id":%d}`, bank, second), http.StatusOK)
	if filled.SourceLesson == nil || filled.SourceLesson.ID != second || filled.Status != 1 {
		t.Fatalf("источник не заполнен: %+v", filled)
	}
	// Чужой преподаватель не подставит свой урок в словарь — у него
	// словаря нет вовсе (роль)
	saveUserTerm(t, h, otherTeacher, fmt.Sprintf(`{"term_id":%d,"source_lesson_id":%d}`, run, own), http.StatusForbidden)

	if _, err := conn.Exec(ctx, `DELETE FROM lessons WHERE id = $1`, own); err != nil {
		t.Fatal(err)
	}
	page := listUserTerms(t, h, student, "?status=4")
	if len(page.Items) != 1 || page.Items[0].TermID != run || page.Items[0].SourceLesson != nil {
		t.Fatalf("после отмены урока: %+v", page.Items)
	}
}

// Страницы словаря: курсор проходит все слова по разу, новые сверху.
func TestUserTermsPagination(t *testing.T) {
	a, _, student := termsAPI(t, "")
	h := a.Router()
	words := []string{"run", "bank", "house", "tree", "river"}
	for _, word := range words {
		saveUserTerm(t, h, student, fmt.Sprintf(`{"term_id":%d}`, ensureTerm(t, a, word)), http.StatusCreated)
	}
	var got []string
	query := "?limit=2"
	for pages := 0; ; pages++ {
		if pages > len(words) {
			t.Fatal("курсор зациклился")
		}
		page := listUserTerms(t, h, student, query)
		for _, it := range page.Items {
			got = append(got, it.Lemma)
		}
		if page.NextCursor == nil {
			break
		}
		query = "?limit=2&cursor=" + *page.NextCursor
	}
	slices.Reverse(words)
	if !slices.Equal(got, words) {
		t.Fatalf("страницы: %v, ждали %v", got, words)
	}
}

// Раскраска: lookup отдаёт только статусы вошедшего, по леммам, и
// выдерживает пачку слов двухчасового транскрипта.
func TestUserTermsLookupReturnsOnlyCallersStatuses(t *testing.T) {
	a, conn, student := termsAPI(t, "")
	h := a.Router()
	_, other := securityUser(t, a, conn, store.RoleStudent, "neighbour")
	run, bank, house := ensureTerm(t, a, "run"), ensureTerm(t, a, "bank"), ensureTerm(t, a, "house")
	for _, it := range []struct {
		cookie *http.Cookie
		term   int64
		status int
	}{{student, run, 2}, {student, house, 5}, {other, bank, 4}, {other, run, 1}} {
		saveUserTerm(t, h, it.cookie, fmt.Sprintf(`{"term_id":%d,"status":%d}`, it.term, it.status), http.StatusCreated)
	}

	words := []string{"running", "ran", "banks", "house", "the", "zzyzx", "42", "", "two words", "-run"}
	mine := lookupStatuses(t, h, student, words)
	if want := map[string]int{"run": 2, "house": 5}; !maps.Equal(mine.Statuses, want) {
		t.Fatalf("статусы: %v, ждали %v", mine.Statuses, want)
	}
	if want := map[string]string{"running": "run", "ran": "run", "banks": "bank"}; !maps.Equal(mine.Lemmas, want) {
		t.Fatalf("леммы: %v, ждали %v", mine.Lemmas, want)
	}
	if theirs := lookupStatuses(t, h, other, words); !maps.Equal(theirs.Statuses, map[string]int{"run": 1, "bank": 4}) {
		t.Fatalf("статусы соседа: %v", theirs.Statuses)
	}
	if empty := lookupStatuses(t, h, student, nil); len(empty.Statuses) != 0 || len(empty.Lemmas) != 0 {
		t.Fatalf("пустая пачка: %+v", empty)
	}

	// Пачка размером со словарь двухчасового урока: 2000 разных слов,
	// половина — в словаре студента
	ctx := context.Background()
	var studentID int64
	if err := conn.QueryRow(ctx, `SELECT id FROM users WHERE name = 'clicker'`).Scan(&studentID); err != nil {
		t.Fatal(err)
	}
	batch := make([]string, 0, maxLookupWords)
	want := map[string]int{"run": 2, "house": 5}
	var lemmas []string
	var statuses []int
	for i := range maxLookupWords - 2 {
		// qaaaa, qaaab, …: буквы вместо цифр номера
		word := "q" + strings.Map(func(r rune) rune { return 'a' + (r - '0') }, fmt.Sprintf("%04d", i))
		batch = append(batch, word)
		if lem := lemma.Lemma(word); i%2 == 0 && want[lem] == 0 {
			want[lem] = 1 + i%5
			lemmas, statuses = append(lemmas, lem), append(statuses, 1+i%5)
		}
	}
	batch = append(batch, "running", "house")
	if _, err := conn.Exec(ctx,
		`WITH t AS (INSERT INTO terms (lemma, surface) SELECT l, l FROM unnest($2::text[]) l
		            ON CONFLICT (lang, lemma) DO UPDATE SET surface = terms.surface RETURNING id, lemma)
		 INSERT INTO user_terms (user_id, term_id, status)
		 SELECT $1, t.id, s.status FROM t JOIN unnest($2::text[], $3::int[]) AS s(lemma, status) USING (lemma)`,
		studentID, lemmas, statuses); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	big := lookupStatuses(t, h, student, batch)
	t.Logf("lookup %d слов: %v", len(batch), time.Since(started))
	if !maps.Equal(big.Statuses, want) {
		t.Fatalf("большая пачка: %d статусов, ждали %d", len(big.Statuses), len(want))
	}
}

func TestUserTermsAccess(t *testing.T) {
	a, conn, student := termsAPI(t, "")
	h := a.Router()
	termID := ensureTerm(t, a, "run")
	saveUserTerm(t, h, student, fmt.Sprintf(`{"term_id":%d,"status":2}`, termID), http.StatusCreated)
	routes := []struct{ method, path, body string }{
		{"POST", "/user-terms", fmt.Sprintf(`{"term_id":%d}`, termID)},
		{"GET", "/user-terms", ""},
		{"DELETE", fmt.Sprintf("/user-terms/%d", termID), ""},
		{"POST", "/user-terms/lookup", `{"words":["run"]}`},
	}
	for _, rt := range routes {
		expectStatus(t, securityRequest(h, rt.method, rt.path, rt.body, nil), http.StatusUnauthorized)
	}
	// Личный словарь — у студента (04-api.md): у гостя события учётка до
	// конца события, у преподавателя и админа словаря нет
	for _, role := range []string{store.RoleTeacher, store.RoleAdmin, store.RoleGuest} {
		_, cookie := securityUser(t, a, conn, role, "role-"+role)
		for _, rt := range routes {
			expectStatus(t, securityRequest(h, rt.method, rt.path, rt.body, cookie), http.StatusForbidden)
		}
		// Попап у них работает, статуса в ответе нет
		w := securityRequest(h, "POST", "/terms/click", `{"surface":"run"}`, cookie)
		expectStatus(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), `"status":null`) {
			t.Fatalf("%s: статус в клике %s", role, w.Body.String())
		}
	}
	// Чужое слово другой студент не удалит и не увидит
	_, other := securityUser(t, a, conn, store.RoleStudent, "intruder")
	expectStatus(t, securityRequest(h, "DELETE", fmt.Sprintf("/user-terms/%d", termID), "", other), http.StatusNotFound)
	if page := listUserTerms(t, h, other, ""); len(page.Items) != 0 {
		t.Fatalf("чужой словарь виден: %+v", page.Items)
	}
	if page := listUserTerms(t, h, student, ""); len(page.Items) != 1 || page.Items[0].Status != 2 {
		t.Fatalf("словарь владельца после чужого DELETE: %+v", page.Items)
	}
	expectStatus(t, securityRequest(h, "DELETE", "/user-terms/abc", "", student), http.StatusBadRequest)
}

// 0015 откатывается начисто и накатывается повторно; terms при откате
// остаются.
func TestUserTermsMigrationReversible(t *testing.T) {
	_, conn := securityDB(t)
	ctx := context.Background()
	state := func() string {
		t.Helper()
		var cols, idx string
		if err := conn.QueryRow(ctx, `SELECT coalesce(string_agg(column_name, ',' ORDER BY column_name), '')
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'user_terms'`).Scan(&cols); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, `SELECT coalesce(string_agg(indexname, ',' ORDER BY indexname), '')
			FROM pg_indexes WHERE schemaname = current_schema() AND tablename = 'user_terms'`).Scan(&idx); err != nil {
			t.Fatal(err)
		}
		return cols + " " + idx
	}
	want := "added_at,source_lesson_id,status,term_id,updated_at,user_id user_terms_pkey,user_terms_user_added_idx"
	if got := state(); got != want {
		t.Fatalf("схема 0015: %s", got)
	}
	// Пункты cloze (0022) и карточки FSRS (0021) ссылаются на user_terms:
	// сначала их откат
	testdb.Apply(t, conn, "0022_cloze.down.sql")
	testdb.Apply(t, conn, "0021_srs.down.sql")
	testdb.Apply(t, conn, "0015_user_terms.down.sql")
	if got := state(); got != " " {
		t.Fatalf("down оставил %q", got)
	}
	var terms bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('terms') IS NOT NULL`).Scan(&terms); err != nil || !terms {
		t.Fatalf("откат 0015 задел terms: %v", err)
	}
	testdb.Apply(t, conn, "0015_user_terms.up.sql")
	testdb.Apply(t, conn, "0021_srs.up.sql")
	testdb.Apply(t, conn, "0022_cloze.up.sql")
	if got := state(); got != want {
		t.Fatalf("повторный up: %s", got)
	}
}
