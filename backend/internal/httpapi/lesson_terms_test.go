package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/dictionary"
	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/testdb"
	"github.com/IEZhu/class/backend/internal/worker"
)

// sceneFixture — сцена Excalidraw с текстом на доске (разбор — в
// internal/lessonterms): знакомые слова — cat ×2, run ×2 и ещё семь по
// одному, Blorpy незнакомо, удалённый elephant не в счёт.
const sceneFixture = "../lessonterms/testdata/scene.json"

var sceneWords = map[string]int{
	"cat": 2, "run": 2, "beautiful": 1, "difficult": 1, "extraordinarily": 1,
	"fast": 1, "homework": 1, "house": 1, "school": 1,
}

type pickerFixture struct {
	a    *API
	h    http.Handler
	conn *pgx.Conn

	teacherID, aliceID, bobID, outsiderID int64
	teacher, otherTeacher, admin          *http.Cookie
	alice, bob, outsider                  *http.Cookie
	// lessonID — урок группы с Алисой и Бобом
	lessonID int64
}

func newPickerFixture(t *testing.T) *pickerFixture {
	t.Helper()
	a, conn := securityDB(t)
	importFixture(t, a, dictionaryFixture, dictionary.Options{Source: "fixture"})
	f := &pickerFixture{a: a, h: a.Router(), conn: conn}
	f.teacherID, f.teacher = securityUser(t, a, conn, store.RoleTeacher, "teacher")
	_, f.otherTeacher = securityUser(t, a, conn, store.RoleTeacher, "other-teacher")
	_, f.admin = securityUser(t, a, conn, store.RoleAdmin, "admin")
	f.aliceID, f.alice = securityUser(t, a, conn, store.RoleStudent, "alice")
	f.bobID, f.bob = securityUser(t, a, conn, store.RoleStudent, "bob")
	f.outsiderID, f.outsider = securityUser(t, a, conn, store.RoleStudent, "outsider")
	f.lessonID = lessonWithStudent(t, a, conn, f.teacherID, "B1 group", f.aliceID, f.bobID)
	return f
}

func (f *pickerFixture) path(lessonID int64, tail string) string {
	return fmt.Sprintf("/lessons/%d%s", lessonID, tail)
}

func (f *pickerFixture) candidates(t *testing.T, lessonID int64) []candidateResponse {
	t.Helper()
	w := securityRequest(f.h, "GET", f.path(lessonID, "/term-candidates"), "", f.teacher)
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"items":[`) {
		t.Fatalf("items — список, не null: %s", w.Body.String())
	}
	var resp candidatesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Items
}

// bySource — кандидаты одного источника: лемма → строка.
func bySource(items []candidateResponse, source string) map[string]candidateResponse {
	out := map[string]candidateResponse{}
	for _, c := range items {
		if c.Source == source {
			out[c.Lemma] = c
		}
	}
	return out
}

func (f *pickerFixture) clickOnLesson(t *testing.T, cookie *http.Cookie, surface string) termClickResponse {
	t.Helper()
	return click(t, f.h, cookie, fmt.Sprintf(`{"surface":%q,"lesson_id":%d}`, surface, f.lessonID))
}

// openBoard — доска урока со сценой-образцом, как её сохранили бы
// участники (PUT /lessons/{id}/whiteboard/scene).
func (f *pickerFixture) openBoard(t *testing.T, lessonID int64) {
	t.Helper()
	ctx := context.Background()
	raw, err := os.ReadFile(sceneFixture)
	if err != nil {
		t.Fatal(err)
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		t.Fatal(err)
	}
	room, err := newWhiteboardRoom()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.store.OpenWhiteboard(ctx, lessonID, room); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.store.SaveWhiteboardScene(ctx, lessonID, elements, maxSceneBytes, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func (f *pickerFixture) userTerm(t *testing.T, userID, termID int64) (status int, source *int64, updated time.Time) {
	t.Helper()
	err := f.conn.QueryRow(context.Background(),
		`SELECT status, source_lesson_id, updated_at FROM user_terms WHERE user_id = $1 AND term_id = $2`,
		userID, termID).Scan(&status, &source, &updated)
	if err != nil {
		t.Fatalf("user_terms (%d, %d): %v", userID, termID, err)
	}
	return status, source, updated
}

func (f *pickerFixture) userTermCount(t *testing.T, userID int64) int {
	t.Helper()
	var n int
	if err := f.conn.QueryRow(context.Background(),
		`SELECT count(*) FROM user_terms WHERE user_id = $1`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// DoD S3-7, клики: клик участника урока по знакомому слову заводит
// кандидата source='click', клики по другим формам и параллельные клики
// растят один счётчик; незнакомое слово, клик без урока, клики
// преподавателя и чужого студента кандидатов не пишут.
func TestClickCandidatesFromParticipants(t *testing.T) {
	f := newPickerFixture(t)

	first := f.clickOnLesson(t, f.alice, "Running")
	if first.TermID == 0 || first.Lemma != "run" {
		t.Fatalf("клик: %+v", first)
	}
	got := bySource(f.candidates(t, f.lessonID), store.CandidateClick)
	if c := got["run"]; len(got) != 1 || c.Count != 1 || c.Surface != "Running" || c.TermID != first.TermID || c.Confirmed {
		t.Fatalf("после первого клика: %+v", got)
	}

	f.clickOnLesson(t, f.alice, "ran")
	f.clickOnLesson(t, f.bob, "runs")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := securityRequest(f.h, "POST", "/terms/click", fmt.Sprintf(`{"surface":"run","lesson_id":%d}`, f.lessonID), f.bob)
			if w.Code != http.StatusOK {
				t.Errorf("параллельный клик: %d %s", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()

	if unknown := f.clickOnLesson(t, f.alice, "blorpy"); unknown.TermID != 0 {
		t.Fatalf("незнакомое слово завело термин: %+v", unknown)
	}
	click(t, f.h, f.alice, `{"surface":"bank"}`)
	f.clickOnLesson(t, f.teacher, "polish")
	expectStatus(t, securityRequest(f.h, "POST", "/terms/click",
		fmt.Sprintf(`{"surface":"building","lesson_id":%d}`, f.lessonID), f.outsider), http.StatusForbidden)

	got = bySource(f.candidates(t, f.lessonID), store.CandidateClick)
	if c := got["run"]; len(got) != 1 || c.Count != 23 || c.Surface != "Running" || !c.LastSeenAt.After(c.FirstSeenAt) {
		t.Fatalf("кандидаты кликов: %+v; ждали только run ×23", got)
	}
	var addedBy int64
	if err := f.conn.QueryRow(context.Background(),
		`SELECT added_by FROM lesson_term_candidates WHERE lesson_id = $1`, f.lessonID).Scan(&addedBy); err != nil || addedBy != f.aliceID {
		t.Fatalf("added_by %d (%v), ждали первого кликнувшего", addedBy, err)
	}
}

// DoD S3-7, доска: разбор сцены по кнопке преподавателя и по концу урока
// (джоба whiteboard_terms) даёт кандидатов source='whiteboard' со
// счётчиком вхождений; повтор ничего не меняет.
func TestWhiteboardCandidatesFromScene(t *testing.T) {
	f := newPickerFixture(t)
	ctx := context.Background()
	f.openBoard(t, f.lessonID)

	collect := func(lessonID int64, cookie *http.Cookie, want int) {
		t.Helper()
		w := securityRequest(f.h, "POST", f.path(lessonID, "/term-candidates/whiteboard"), "", cookie)
		expectStatus(t, w, want)
		if want != http.StatusOK {
			return
		}
		var resp whiteboardCandidatesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Candidates != len(sceneWords) {
			t.Fatalf("разбор доски: %s (%v), ждали %d", w.Body.String(), err, len(sceneWords))
		}
	}
	check := func(lessonID int64) {
		t.Helper()
		got := bySource(f.candidates(t, lessonID), store.CandidateWhiteboard)
		if len(got) != len(sceneWords) {
			t.Fatalf("кандидаты доски: %+v", got)
		}
		for lem, n := range sceneWords {
			if got[lem].Count != n {
				t.Fatalf("%s: %+v, ждали ×%d", lem, got[lem], n)
			}
		}
		if got["run"].Surface != "Running" || got["house"].Surface != "houses" {
			t.Fatalf("формы первой встречи: %+v %+v", got["run"], got["house"])
		}
	}
	collect(f.lessonID, f.teacher, http.StatusOK)
	check(f.lessonID)
	collect(f.lessonID, f.teacher, http.StatusOK)
	check(f.lessonID)

	collect(f.lessonID, f.otherTeacher, http.StatusForbidden)
	collect(f.lessonID, f.alice, http.StatusForbidden)
	collect(f.lessonID, nil, http.StatusUnauthorized)
	collect(999999, f.teacher, http.StatusNotFound)
	// У урока без доски разбирать нечего
	bare := lessonWithStudent(t, f.a, f.conn, f.teacherID, "no board", f.aliceID)
	w := securityRequest(f.h, "POST", f.path(bare, "/term-candidates/whiteboard"), "", f.teacher)
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"candidates":0`) {
		t.Fatalf("урок без доски: %s", w.Body.String())
	}

	// Конец урока: окно комнаты закрылось — уборка записей переводит
	// урок из live, и в той же транзакции ставит разбор его доски
	ended := lessonWithStudent(t, f.a, f.conn, f.teacherID, "ended", f.aliceID)
	f.openBoard(t, ended)
	for _, id := range []int64{ended, bare} {
		if _, err := f.conn.Exec(ctx, `UPDATE lessons SET status = 'live',
			starts_at = now() - interval '3 hours', ends_at = now() - interval '2 hours' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := f.a.store.SweepRecordings(ctx); err != nil || n != 2 {
		t.Fatalf("уборка завершила %d уроков (%v), ждали 2", n, err)
	}
	var queued []int64
	rows, err := f.conn.Query(ctx, `SELECT (payload->>'lesson_id')::bigint FROM jobs WHERE kind = $1`, store.JobWhiteboardTerms)
	if err != nil {
		t.Fatal(err)
	}
	if queued, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil || len(queued) != 1 || queued[0] != ended {
		t.Fatalf("джобы разбора доски %v (%v), ждали одну для урока %d", queued, err, ended)
	}
	runner := jobs.NewRunner(f.a.store.Pool(), jobs.Config{WorkerID: "test-worker"})
	worker.RegisterLessonTerms(runner, f.a.store)
	if worked, err := runner.RunOnce(ctx); err != nil || !worked {
		t.Fatalf("RunOnce = %v, %v", worked, err)
	}
	check(ended)
	var left int
	if err := f.conn.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1`, store.JobWhiteboardTerms).Scan(&left); err != nil || left != 0 {
		t.Fatalf("джоба не снята: %d (%v)", left, err)
	}
}

// DoD S3-7, ручной ввод: только преподаватель урока; не слово
// и незнакомое возвращаются, формы одной леммы — один кандидат,
// повтор ничего не меняет.
func TestManualCandidatesByLessonTeacher(t *testing.T) {
	f := newPickerFixture(t)
	// the нет в OEWN, но есть статья датасета — знакомое (ADR-017);
	// zzyzxq датасет при импорте отбросил — незнакомое
	body := `{"words":["Beautiful","take off","blorpy","houses","house","The","zzyzxq",""]}`
	add := func(cookie *http.Cookie) manualCandidatesResponse {
		t.Helper()
		w := securityRequest(f.h, "POST", f.path(f.lessonID, "/term-candidates"), body, cookie)
		expectStatus(t, w, http.StatusOK)
		var resp manualCandidatesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	want := `{[{beautiful Beautiful} {house houses} {the The}] [{take off not_a_word} { not_a_word} {blorpy unknown} {zzyzxq unknown}]}`
	for range 2 {
		resp := add(f.teacher)
		var added []string
		for _, a := range resp.Added {
			if a.TermID == 0 {
				t.Fatalf("кандидат без термина: %+v", a)
			}
			added = append(added, fmt.Sprintf("{%s %s}", a.Lemma, a.Surface))
		}
		var rejected []string
		for _, r := range resp.Rejected {
			rejected = append(rejected, fmt.Sprintf("{%s %s}", r.Word, r.Reason))
		}
		if got := fmt.Sprintf("{[%s] [%s]}", strings.Join(added, " "), strings.Join(rejected, " ")); got != want {
			t.Fatalf("ручной ввод: %s\nждали %s", got, want)
		}
	}
	got := bySource(f.candidates(t, f.lessonID), store.CandidateManual)
	if len(got) != 3 || got["house"].Count != 1 || got["house"].Surface != "houses" {
		t.Fatalf("кандидаты ручного ввода: %+v", got)
	}

	for _, c := range []struct {
		cookie *http.Cookie
		path   string
		body   string
		want   int
	}{
		{nil, f.path(f.lessonID, "/term-candidates"), body, http.StatusUnauthorized},
		{f.alice, f.path(f.lessonID, "/term-candidates"), body, http.StatusForbidden},
		{f.admin, f.path(f.lessonID, "/term-candidates"), body, http.StatusForbidden},
		{f.otherTeacher, f.path(f.lessonID, "/term-candidates"), body, http.StatusForbidden},
		{f.teacher, "/lessons/999999/term-candidates", body, http.StatusNotFound},
		{f.teacher, "/lessons/abc/term-candidates", body, http.StatusBadRequest},
		{f.teacher, f.path(f.lessonID, "/term-candidates"), `{"words":[]}`, http.StatusBadRequest},
		{f.teacher, f.path(f.lessonID, "/term-candidates"),
			`{"words":[` + strings.TrimSuffix(strings.Repeat(`"run",`, maxManualWords+1), ",") + `]}`, http.StatusBadRequest},
	} {
		expectStatus(t, securityRequest(f.h, "POST", c.path, c.body, c.cookie), c.want)
	}
	// Чеклист кандидатов студенты не видят
	for _, cookie := range []*http.Cookie{f.alice, f.otherTeacher, f.admin} {
		expectStatus(t, securityRequest(f.h, "GET", f.path(f.lessonID, "/term-candidates"), "", cookie), http.StatusForbidden)
	}
	expectStatus(t, securityRequest(f.h, "GET", f.path(f.lessonID, "/term-candidates"), "", nil), http.StatusUnauthorized)
}

// DoD S3-8: чеклист → lesson_terms → назначение выбранным или всем
// студентам урока в user_terms(status=1, source_lesson_id) через
// SaveUserTerm; повтор идемпотентен, выученное слово не откатывается.
func TestPickerConfirmAndAssign(t *testing.T) {
	f := newPickerFixture(t)
	ctx := context.Background()
	w := securityRequest(f.h, "POST", f.path(f.lessonID, "/term-candidates"), `{"words":["run","house","bank"]}`, f.teacher)
	expectStatus(t, w, http.StatusOK)
	var manual manualCandidatesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &manual); err != nil || len(manual.Added) != 3 {
		t.Fatalf("кандидаты: %s", w.Body.String())
	}
	run, house, bank := manual.Added[0].TermID, manual.Added[1].TermID, manual.Added[2].TermID
	// Алиса уже учит run и дошла до 4 — назначение этого не отменит
	four := 4
	if _, err := f.a.store.SaveUserTerm(ctx, f.aliceID, run, &four, nil); err != nil {
		t.Fatal(err)
	}

	setTerms := func(body string, want int) []lessonTermResponse {
		t.Helper()
		w := securityRequest(f.h, "PUT", f.path(f.lessonID, "/terms"), body, f.teacher)
		expectStatus(t, w, want)
		var resp lessonTermsResponse
		if want == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
		}
		return resp.Items
	}
	lemmas := func(items []lessonTermResponse) string {
		var out []string
		for _, it := range items {
			out = append(out, it.Lemma)
		}
		return strings.Join(out, ",")
	}
	setTerms(fmt.Sprintf(`{"term_ids":[%d,%d,999999]}`, run, house), http.StatusBadRequest)
	setTerms(`{}`, http.StatusBadRequest)
	if got := lemmas(setTerms(fmt.Sprintf(`{"term_ids":[%d,%d,%d,%d]}`, run, house, bank, run), http.StatusOK)); got != "bank,house,run" {
		t.Fatalf("словарь урока: %s", got)
	}
	if got := lemmas(setTerms(fmt.Sprintf(`{"term_ids":[%d,%d]}`, run, house), http.StatusOK)); got != "house,run" {
		t.Fatalf("снятый bank остался: %s", got)
	}
	confirmed := bySource(f.candidates(t, f.lessonID), store.CandidateManual)
	if !confirmed["run"].Confirmed || !confirmed["house"].Confirmed || confirmed["bank"].Confirmed {
		t.Fatalf("отметки чеклиста: %+v", confirmed)
	}

	assign := func(cookie *http.Cookie, body string, want int) assignLessonTermsResponse {
		t.Helper()
		w := securityRequest(f.h, "POST", f.path(f.lessonID, "/terms/assignments"), body, cookie)
		expectStatus(t, w, want)
		var resp assignLessonTermsResponse
		if want == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
		}
		return resp
	}
	toAlice := fmt.Sprintf(`{"student_ids":[%d,%d]}`, f.aliceID, f.aliceID)
	if got := assign(f.teacher, toAlice, http.StatusOK); got != (assignLessonTermsResponse{Students: 1, Terms: 2, Added: 1, Kept: 1}) {
		t.Fatalf("назначение Алисе: %+v", got)
	}
	status, source, runUpdated := f.userTerm(t, f.aliceID, run)
	if status != 4 || source == nil || *source != f.lessonID {
		t.Fatalf("run у Алисы: статус %d, источник %v; ждали 4 и урок", status, source)
	}
	if status, source, _ := f.userTerm(t, f.aliceID, house); status != 1 || source == nil || *source != f.lessonID {
		t.Fatalf("house у Алисы: статус %d, источник %v", status, source)
	}
	if n := f.userTermCount(t, f.bobID); n != 0 {
		t.Fatalf("Бобу не назначали, а у него %d слов", n)
	}
	if got := assign(f.teacher, toAlice, http.StatusOK); got != (assignLessonTermsResponse{Students: 1, Terms: 2, Added: 0, Kept: 2}) {
		t.Fatalf("повтор назначения: %+v", got)
	}
	if _, _, again := f.userTerm(t, f.aliceID, run); !again.Equal(runUpdated) {
		t.Fatalf("повтор тронул updated_at: %v → %v", runUpdated, again)
	}
	if got := assign(f.teacher, `{"all":true}`, http.StatusOK); got != (assignLessonTermsResponse{Students: 2, Terms: 2, Added: 2, Kept: 2}) {
		t.Fatalf("назначение всем: %+v", got)
	}
	for _, term := range []int64{run, house} {
		if status, source, _ := f.userTerm(t, f.bobID, term); status != 1 || source == nil || *source != f.lessonID {
			t.Fatalf("у Боба термин %d: статус %d, источник %v", term, status, source)
		}
	}
	if n := f.userTermCount(t, f.outsiderID); n != 0 {
		t.Fatalf("чужому студенту назначено %d слов", n)
	}
	// Слово видно в словаре студента с уроком-источником
	page := listUserTerms(t, f.h, f.bob, "")
	if len(page.Items) != 2 || page.Items[0].SourceLesson == nil || page.Items[0].SourceLesson.ID != f.lessonID {
		t.Fatalf("словарь Боба: %+v", page.Items)
	}

	for _, c := range []struct {
		cookie *http.Cookie
		body   string
		want   int
	}{
		{f.teacher, `{}`, http.StatusBadRequest},
		{f.teacher, `{"all":false,"student_ids":[]}`, http.StatusBadRequest},
		{f.teacher, fmt.Sprintf(`{"all":true,"student_ids":[%d]}`, f.aliceID), http.StatusBadRequest},
		{f.teacher, fmt.Sprintf(`{"student_ids":[%d]}`, f.teacherID), http.StatusBadRequest},
		{f.teacher, fmt.Sprintf(`{"student_ids":[%d,%d]}`, f.aliceID, f.outsiderID), http.StatusBadRequest},
		{f.otherTeacher, `{"all":true}`, http.StatusForbidden},
		{f.alice, `{"all":true}`, http.StatusForbidden},
		{f.admin, `{"all":true}`, http.StatusForbidden},
		{nil, `{"all":true}`, http.StatusUnauthorized},
	} {
		assign(c.cookie, c.body, c.want)
	}
	for _, cookie := range []*http.Cookie{f.alice, f.otherTeacher} {
		expectStatus(t, securityRequest(f.h, "PUT", f.path(f.lessonID, "/terms"), fmt.Sprintf(`{"term_ids":[%d]}`, run), cookie), http.StatusForbidden)
	}

	// Пустой словарь урока и урок без студентов — 409
	empty := lessonWithStudent(t, f.a, f.conn, f.teacherID, "empty", f.aliceID)
	expectStatus(t, securityRequest(f.h, "POST", f.path(empty, "/terms/assignments"), `{"all":true}`, f.teacher), http.StatusConflict)
	event, err := f.a.store.CreateLesson(ctx, nil, "Open event", f.teacherID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, securityRequest(f.h, "POST", f.path(event.ID, "/terms/assignments"), `{"all":true}`, f.teacher), http.StatusConflict)

	// Словарь урока читают его участники
	for cookie, want := range map[*http.Cookie]int{
		f.alice: http.StatusOK, f.teacher: http.StatusOK, f.outsider: http.StatusForbidden,
		f.otherTeacher: http.StatusForbidden,
	} {
		w := securityRequest(f.h, "GET", f.path(f.lessonID, "/terms"), "", cookie)
		expectStatus(t, w, want)
		if want == http.StatusOK && !strings.Contains(w.Body.String(), `"lemma":"house"`) {
			t.Fatalf("словарь урока: %s", w.Body.String())
		}
	}
	expectStatus(t, securityRequest(f.h, "GET", "/lessons/999999/terms", "", f.teacher), http.StatusNotFound)
	expectStatus(t, securityRequest(f.h, "GET", f.path(f.lessonID, "/terms"), "", nil), http.StatusUnauthorized)

	// Отмена урока уносит его кандидатов и словарь, слова у студентов остаются
	if _, err := f.conn.Exec(ctx, `DELETE FROM lessons WHERE id = $1`, f.lessonID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := f.conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM lesson_term_candidates WHERE lesson_id = $1)
		+ (SELECT count(*) FROM lesson_terms WHERE lesson_id = $1)`, f.lessonID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("после удаления урока осталось %d строк (%v)", left, err)
	}
	if n := f.userTermCount(t, f.bobID); n != 2 {
		t.Fatalf("словарь Боба после удаления урока: %d", n)
	}
}

// Интеграция S3-8 и S4-1 (D-3): назначение словаря урока идёт через
// store.SaveUserTerm, поэтому каждое новое назначенное слово получает
// карточку FSRS в транзакции слова и сразу встаёт в очередь повторений.
// Слову, которое студент уже учит, график не сбрасывается; повтор
// назначения карточек не множит.
func TestAssignedLessonTermsGetSRSCards(t *testing.T) {
	f := newPickerFixture(t)
	ctx := context.Background()
	useTestClock(f.a)
	w := securityRequest(f.h, "POST", f.path(f.lessonID, "/term-candidates"), `{"words":["run","house"]}`, f.teacher)
	expectStatus(t, w, http.StatusOK)
	var manual manualCandidatesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &manual); err != nil || len(manual.Added) != 2 {
		t.Fatalf("кандидаты: %s", w.Body.String())
	}
	run, house := manual.Added[0].TermID, manual.Added[1].TermID
	// Алиса уже учит run, и карточку повторяли: назначение её не трогает
	if _, err := f.a.store.SaveUserTerm(ctx, f.aliceID, run, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(ctx, `UPDATE srs_cards SET reps = 3, state = 'review' WHERE user_id = $1 AND term_id = $2`,
		f.aliceID, run); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, securityRequest(f.h, "PUT", f.path(f.lessonID, "/terms"),
		fmt.Sprintf(`{"term_ids":[%d,%d]}`, run, house), f.teacher), http.StatusOK)

	assignAll := func() assignLessonTermsResponse {
		t.Helper()
		w := securityRequest(f.h, "POST", f.path(f.lessonID, "/terms/assignments"), `{"all":true}`, f.teacher)
		expectStatus(t, w, http.StatusOK)
		var resp assignLessonTermsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	// Карточка нового слова — из той же транзакции, что и строка user_terms
	newCard := func(userID, termID int64) bool {
		t.Helper()
		var ok bool
		if err := f.conn.QueryRow(ctx,
			`SELECT c.due_at = ut.added_at AND c.created_at = ut.added_at AND c.state = 'new' AND c.reps = 0
			 FROM srs_cards c JOIN user_terms ut USING (user_id, term_id)
			 WHERE c.user_id = $1 AND c.term_id = $2`, userID, termID).Scan(&ok); err != nil {
			t.Fatalf("карточка (%d, %d): %v", userID, termID, err)
		}
		return ok
	}
	cardsOf := func() map[[2]int64]int {
		t.Helper()
		out := map[[2]int64]int{}
		for _, u := range []int64{f.aliceID, f.bobID} {
			for _, term := range []int64{run, house} {
				cards, reviews := srsCardCount(t, f.conn, u, term)
				if reviews != 0 {
					t.Fatalf("назначение записало оценку (%d, %d)", u, term)
				}
				out[[2]int64{u, term}] = cards
			}
		}
		return out
	}

	if got := assignAll(); got != (assignLessonTermsResponse{Students: 2, Terms: 2, Added: 3, Kept: 1}) {
		t.Fatalf("назначение всем: %+v", got)
	}
	before := cardsOf()
	for pair, n := range before {
		if n != 1 {
			t.Fatalf("у пары %v карточек: %d", pair, n)
		}
	}
	for _, pair := range [][2]int64{{f.aliceID, house}, {f.bobID, run}, {f.bobID, house}} {
		if !newCard(pair[0], pair[1]) {
			t.Fatalf("назначенное слово %v без карточки из транзакции слова", pair)
		}
	}
	var reps int
	if err := f.conn.QueryRow(ctx, `SELECT reps FROM srs_cards WHERE user_id = $1 AND term_id = $2`,
		f.aliceID, run).Scan(&reps); err != nil || reps != 3 {
		t.Fatalf("график run у Алисы сброшен: reps %d (%v)", reps, err)
	}

	if got := assignAll(); got != (assignLessonTermsResponse{Students: 2, Terms: 2, Added: 0, Kept: 4}) {
		t.Fatalf("повтор назначения: %+v", got)
	}
	for pair, n := range cardsOf() {
		if n != before[pair] {
			t.Fatalf("повтор назначения изменил карточки %v: %d → %d", pair, before[pair], n)
		}
	}

	// Назначенные слова — в очереди повторений студента
	q := fetchQueue(t, f.h, f.bob, "")
	if got := strings.Join(queueLemmas(q), ","); got != "run,house" && got != "house,run" {
		t.Fatalf("очередь Боба: %s", got)
	}
}

// 0019 откатывается начисто (вместе с ждущими разборами досок)
// и накатывается повторно; terms и user_terms при откате остаются.
func TestLessonTermsMigrationReversible(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	state := func() string {
		t.Helper()
		var cols, idx string
		if err := conn.QueryRow(ctx, `SELECT coalesce(string_agg(table_name || '.' || column_name, ',' ORDER BY table_name, column_name), '')
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name IN ('lesson_term_candidates', 'lesson_terms')`).Scan(&cols); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, `SELECT coalesce(string_agg(indexname, ',' ORDER BY indexname), '')
			FROM pg_indexes WHERE schemaname = current_schema()
			  AND tablename IN ('lesson_term_candidates', 'lesson_terms')`).Scan(&idx); err != nil {
			t.Fatal(err)
		}
		return cols + " " + idx
	}
	want := "lesson_term_candidates.added_by,lesson_term_candidates.count,lesson_term_candidates.first_seen_at," +
		"lesson_term_candidates.last_seen_at,lesson_term_candidates.lesson_id,lesson_term_candidates.source," +
		"lesson_term_candidates.surface,lesson_term_candidates.term_id,lesson_terms.confirmed_at," +
		"lesson_terms.confirmed_by,lesson_terms.lesson_id,lesson_terms.term_id " +
		"lesson_term_candidates_pkey,lesson_terms_pkey"
	if got := state(); got != want {
		t.Fatalf("схема 0019: %s", got)
	}
	if err := jobs.EnqueueOnce(ctx, a.store.Pool(), store.JobWhiteboardTerms, store.WhiteboardTermsJob{LessonID: 1}, 0); err != nil {
		t.Fatal(err)
	}
	testdb.Apply(t, conn, "0019_lesson_terms.down.sql")
	if got := state(); got != " " {
		t.Fatalf("down оставил %q", got)
	}
	var jobsLeft int
	var kept bool
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM jobs),
		to_regclass('terms') IS NOT NULL AND to_regclass('user_terms') IS NOT NULL`).Scan(&jobsLeft, &kept); err != nil ||
		jobsLeft != 0 || !kept {
		t.Fatalf("откат 0019: джоб %d, terms и user_terms на месте %v (%v)", jobsLeft, kept, err)
	}
	testdb.Apply(t, conn, "0019_lesson_terms.up.sql")
	if got := state(); got != want {
		t.Fatalf("повторный up: %s", got)
	}
}
