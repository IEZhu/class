package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/srs"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/tatoeba"
	"github.com/IEZhu/class/backend/internal/testdb"
)

// testClock — подставленные часы api: тест сам двигает время оценок
// и очереди. Старт — чуть позже настоящего времени базы, иначе
// карточка, заведённая now() базы, была бы ещё не к показу.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func useTestClock(a *API) *testClock {
	c := &testClock{at: time.Now().UTC().Truncate(time.Millisecond).Add(time.Minute)}
	a.now = c.now
	return c
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = at
}

// srsCardCount — карточек и оценок у пары (пользователь, слово).
func srsCardCount(t *testing.T, conn *pgx.Conn, userID, termID int64) (cards, reviews int) {
	t.Helper()
	err := conn.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM srs_cards WHERE user_id = $1 AND term_id = $2),
		        (SELECT count(*) FROM srs_reviews WHERE user_id = $1 AND term_id = $2)`,
		userID, termID).Scan(&cards, &reviews)
	if err != nil {
		t.Fatal(err)
	}
	return cards, reviews
}

func addWord(t *testing.T, a *API, userID int64, word string) int64 {
	t.Helper()
	termID := ensureTerm(t, a, word)
	if _, err := a.store.SaveUserTerm(context.Background(), userID, termID, nil, nil); err != nil {
		t.Fatal(err)
	}
	return termID
}

// DoD S4-1 (закрывает D-3): новое слово словаря получает карточку в той
// же транзакции, что и строку user_terms, — и из попапа, и через
// store.SaveUserTerm, которым назначает слова S3-8; смена статуса
// карточку не трогает, удаление слова уносит её.
func TestSRSCardCreatedWithUserTerm(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	studentID, student := securityUser(t, a, conn, store.RoleStudent, "srs-owner")
	h := a.Router()

	termID := ensureTerm(t, a, "apple")
	saveUserTerm(t, h, student, fmt.Sprintf(`{"term_id":%d}`, termID), http.StatusCreated)
	// now() — время начала транзакции: совпадение due_at и created_at
	// карточки с added_at слова значит, что их записала одна транзакция
	sameTx := func(termID int64) bool {
		t.Helper()
		var ok bool
		err := conn.QueryRow(ctx,
			`SELECT c.due_at = ut.added_at AND c.created_at = ut.added_at AND c.state = 'new'
			        AND c.reps = 0 AND c.lapses = 0 AND c.stability = 0 AND c.last_review_at IS NULL
			 FROM srs_cards c JOIN user_terms ut USING (user_id, term_id)
			 WHERE c.user_id = $1 AND c.term_id = $2`, studentID, termID).Scan(&ok)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !sameTx(termID) {
		t.Fatal("карточка нового слова не та или не из транзакции слова")
	}
	saveUserTerm(t, h, student, fmt.Sprintf(`{"term_id":%d,"status":4}`, termID), http.StatusOK)
	if cards, _ := srsCardCount(t, conn, studentID, termID); cards != 1 || !sameTx(termID) {
		t.Fatalf("смена статуса тронула карточку: %d карточек", cards)
	}

	// Путь назначения слов преподавателем (S3-8) — тот же store.SaveUserTerm
	assigned := ensureTerm(t, a, "house")
	one := 1
	if created, err := a.store.SaveUserTerm(ctx, studentID, assigned, &one, nil); err != nil || !created {
		t.Fatalf("SaveUserTerm: %v, %v", created, err)
	}
	if !sameTx(assigned) {
		t.Fatal("назначенное слово без карточки")
	}

	// Сбой записи карточки откатывает и слово: слова без карточки не бывает
	failing := ensureTerm(t, a, "river")
	if _, err := conn.Exec(ctx, fmt.Sprintf(
		`ALTER TABLE srs_cards ADD CONSTRAINT test_reject CHECK (term_id <> %d)`, failing)); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, securityRequest(h, "POST", "/user-terms", fmt.Sprintf(`{"term_id":%d}`, failing), student),
		http.StatusInternalServerError)
	if st, err := a.store.UserTermStatus(ctx, studentID, failing); err != nil || st != nil {
		t.Fatalf("слово осталось без карточки: %v, %v", st, err)
	}

	expectStatus(t, securityRequest(h, "DELETE", fmt.Sprintf("/user-terms/%d", termID), "", student), http.StatusNoContent)
	if cards, _ := srsCardCount(t, conn, studentID, termID); cards != 0 {
		t.Fatal("карточка пережила удаление слова")
	}
}

// Бэкфилл (S4-1): словам, записанным в обход хука (до миграции 0021),
// карточки заводятся с due = added_at; повторный запуск ничего не
// меняет, а карточки с историей не трогает.
func TestSRSBackfillIdempotent(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	clock := useTestClock(a)
	u1, _ := securityUser(t, a, conn, store.RoleStudent, "backfill-one")
	u2, _ := securityUser(t, a, conn, store.RoleStudent, "backfill-two")
	apple, house := ensureTerm(t, a, "apple"), ensureTerm(t, a, "house")
	if _, err := conn.Exec(ctx, `INSERT INTO user_terms (user_id, term_id, added_at) VALUES
		($1, $3, now() - interval '3 days'), ($1, $4, now() - interval '1 day'), ($2, $3, now())`,
		u1, u2, apple, house); err != nil {
		t.Fatal(err)
	}
	reviewed := addWord(t, a, u2, "river")
	res, err := a.store.ReviewCard(ctx, a.srs, store.Review{
		UserID: u2, TermID: reviewed, Rating: srs.Good, Source: srs.SourceCard, At: clock.now(),
	})
	if err != nil || res.Card.State != srs.StateLearning {
		t.Fatalf("оценка до бэкфилла: %+v, %v", res, err)
	}

	snapshot := func() string {
		t.Helper()
		return queryDump(t, conn, `SELECT user_id, term_id, due_at, stability, difficulty, scheduled_days,
			reps, lapses, state, remaining_steps, last_review_at, created_at FROM srs_cards ORDER BY user_id, term_id`)
	}
	n, err := a.store.BackfillSRSCards(ctx)
	if err != nil || n != 3 {
		t.Fatalf("бэкфилл: %d, %v", n, err)
	}
	var fresh int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM srs_cards c JOIN user_terms ut USING (user_id, term_id)
		WHERE c.due_at = ut.added_at AND c.state = 'new' AND c.reps = 0 AND c.last_review_at IS NULL`).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	if fresh != 3 {
		t.Fatalf("новых карточек с due = added_at: %d", fresh)
	}
	before := snapshot()
	n, err = a.store.BackfillSRSCards(ctx)
	if err != nil || n != 0 {
		t.Fatalf("повторный бэкфилл: %d, %v", n, err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("повторный бэкфилл изменил карточки:\n%s\n%s", before, after)
	}
	if _, reviews := srsCardCount(t, conn, u2, reviewed); reviews != 1 {
		t.Fatalf("журнал оценок после бэкфилла: %d", reviews)
	}
}

func fetchQueue(t *testing.T, h http.Handler, cookie *http.Cookie, query string) queueResponse {
	t.Helper()
	w := securityRequest(h, "GET", "/practice/queue"+query, "", cookie)
	expectStatus(t, w, http.StatusOK)
	var resp queueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func queueLemmas(q queueResponse) []string {
	out := []string{}
	for _, it := range q.Items {
		out = append(out, it.Lemma)
	}
	return out
}

// DoD S4-2: очередь на сидированных due — только свои карточки, чей
// показ наступил, самые просроченные первыми, не больше limit; с
// переводом из кэша, дефинициями и примером Tatoeba на обороте.
func TestPracticeQueue(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	clock := useTestClock(a)
	now := clock.now()
	studentID, student := securityUser(t, a, conn, store.RoleStudent, "queue-owner")
	otherID, other := securityUser(t, a, conn, store.RoleStudent, "queue-other")
	_, teacher := securityUser(t, a, conn, store.RoleTeacher, "queue-teacher")
	h := a.Router()

	due := map[string]time.Time{
		"run":   now.Add(-48 * time.Hour),
		"apple": now.Add(-time.Hour),
		"house": now, // ровно сейчас — уже к показу
		"cat":   now.Add(24 * time.Hour),
	}
	terms := map[string]int64{}
	for word, at := range due {
		terms[word] = addWord(t, a, studentID, word)
		if _, err := conn.Exec(ctx, `UPDATE srs_cards SET due_at = $3 WHERE user_id = $1 AND term_id = $2`,
			studentID, terms[word], at); err != nil {
			t.Fatal(err)
		}
	}
	addWord(t, a, otherID, "run")
	if _, err := conn.Exec(ctx, `UPDATE srs_cards SET due_at = $2 WHERE user_id = $1`, otherID, now.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := a.store.CacheTranslation(ctx, terms["apple"], translationLang,
		store.Translation{Text: "яблоко", Source: "libretranslate", At: now}); err != nil {
		t.Fatal(err)
	}
	var importID int64
	if err := conn.QueryRow(ctx, `INSERT INTO definition_imports (source) VALUES ('fixture') RETURNING id`).Scan(&importID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO definitions (word, lemma, pos, glosses, source_line, import_id)
		VALUES ('apple', 'apple', 'noun', ARRAY['A common fruit.'], 1, $1)`, importID); err != nil {
		t.Fatal(err)
	}
	importTatoeba(t, a, readTatoebaFixture(t).sources("v1"), tatoeba.Options{})

	q := fetchQueue(t, h, student, "")
	if got := fmt.Sprint(queueLemmas(q)); got != "[run apple house]" || q.DueTotal != 3 ||
		q.NextDueAt == nil || !q.NextDueAt.Equal(due["cat"]) {
		t.Fatalf("очередь: %s, due_total %d, next %v", got, q.DueTotal, q.NextDueAt)
	}
	run, apple, house := q.Items[0], q.Items[1], q.Items[2]
	if len(run.Examples) == 0 || run.Examples[0].Attribution.License != tatoeba.License {
		t.Fatalf("пример Tatoeba у run: %+v", run.Examples)
	}
	if apple.Translation == nil || *apple.Translation != "яблоко" || len(apple.Definitions) != 1 ||
		apple.Definitions[0].Glosses[0] != "A common fruit." {
		t.Fatalf("оборот apple: %+v", apple)
	}
	if house.Translation != nil || house.Definitions == nil || house.Examples == nil {
		t.Fatalf("у слова без перевода и статей — null и пустые списки: %+v", house)
	}
	if house.Status != 1 || house.Card.State != srs.StateNew || house.Card.Reps != 0 ||
		!house.Card.DueAt.Equal(now) || house.Card.LastReviewAt != nil {
		t.Fatalf("карточка house: %+v", house)
	}

	if q := fetchQueue(t, h, student, "?limit=2"); fmt.Sprint(queueLemmas(q)) != "[run apple]" || q.DueTotal != 3 {
		t.Fatalf("limit=2: %v, due_total %d", queueLemmas(q), q.DueTotal)
	}
	// Время идёт — подходит и cat; будущих показов не остаётся
	clock.set(due["cat"])
	if q := fetchQueue(t, h, student, ""); q.DueTotal != 4 || q.NextDueAt != nil || q.Items[3].Lemma != "cat" {
		t.Fatalf("через сутки: %v, due_total %d, next %v", queueLemmas(q), q.DueTotal, q.NextDueAt)
	}
	if q := fetchQueue(t, h, other, ""); fmt.Sprint(queueLemmas(q)) != "[run]" || q.DueTotal != 1 {
		t.Fatalf("очередь другого студента: %v", queueLemmas(q))
	}
	for _, bad := range []string{"?limit=0", "?limit=51", "?limit=x"} {
		expectStatus(t, securityRequest(h, "GET", "/practice/queue"+bad, "", student), http.StatusBadRequest)
	}
	expectStatus(t, securityRequest(h, "GET", "/practice/queue", "", teacher), http.StatusForbidden)
	expectStatus(t, securityRequest(h, "GET", "/practice/queue", "", nil), http.StatusUnauthorized)
	// Пустая очередь — пустой список, не null
	_, empty := securityUser(t, a, conn, store.RoleStudent, "queue-empty")
	if w := securityRequest(h, "GET", "/practice/queue", "", empty); w.Body.String() != "{\"items\":[],\"due_total\":0,\"next_due_at\":null}\n" {
		t.Fatalf("пустая очередь: %s", w.Body.String())
	}
}

func postReview(t *testing.T, h http.Handler, cookie *http.Cookie, id string, termID int64, rating string, want int) reviewResponse {
	t.Helper()
	w := securityRequest(h, "POST", "/reviews",
		fmt.Sprintf(`{"review_id":%q,"term_id":%d,"rating":%q}`, id, termID, rating), cookie)
	expectStatus(t, w, want)
	var resp reviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func reviewID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

// DoD S4-3: оценка двигает due_at предсказуемо (шаги 1/10 минут, дальше
// интервалы FSRS в днях), пишет srs_reviews и статус слова; повтор
// review_id засчитывается один раз; чужие и не наступившие карточки
// не оцениваются.
func TestReviewMovesCardAndLogs(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	clock := useTestClock(a)
	studentID, student := securityUser(t, a, conn, store.RoleStudent, "reviewer")
	otherID, other := securityUser(t, a, conn, store.RoleStudent, "reviewer-other")
	_, teacher := securityUser(t, a, conn, store.RoleTeacher, "reviewer-teacher")
	h := a.Router()
	termID := addWord(t, a, studentID, "apple")

	start := clock.now()
	first := postReview(t, h, student, reviewID(1), termID, "good", http.StatusCreated)
	if first.Duplicate || first.Card.State != srs.StateLearning || !first.Card.DueAt.Equal(start.Add(10*time.Minute)) ||
		first.Card.Reps != 1 || first.Status != 2 || first.Rating != srs.Good {
		t.Fatalf("первая оценка good: %+v", first)
	}
	var rating, source string
	var reviewedAt time.Time
	if err := conn.QueryRow(ctx, `SELECT rating, source, reviewed_at FROM srs_reviews
		WHERE user_id = $1 AND term_id = $2 AND client_id = $3`, studentID, termID, reviewID(1)).
		Scan(&rating, &source, &reviewedAt); err != nil {
		t.Fatal(err)
	}
	if rating != "good" || source != "card" || !reviewedAt.Equal(start) {
		t.Fatalf("журнал: %s %s %v", rating, source, reviewedAt)
	}
	if st, err := a.store.UserTermStatus(ctx, studentID, termID); err != nil || st == nil || *st != 2 {
		t.Fatalf("статус слова после оценки: %v, %v", st, err)
	}

	// Повтор того же запроса (сеть, двойное нажатие) — 200, график не тронут
	dup := postReview(t, h, student, reviewID(1), termID, "good", http.StatusOK)
	if !dup.Duplicate || !dup.Card.DueAt.Equal(first.Card.DueAt) || dup.Card.Reps != 1 {
		t.Fatalf("повтор: %+v", dup)
	}
	if _, reviews := srsCardCount(t, conn, studentID, termID); reviews != 1 {
		t.Fatalf("повтор записал оценку: %d", reviews)
	}
	// Тот же ключ с другой оценкой — review_conflict, а не not_due: ключ
	// занят, хотя карточка и не к показу
	w := securityRequest(h, "POST", "/reviews",
		fmt.Sprintf(`{"review_id":%q,"term_id":%d,"rating":"easy"}`, reviewID(1), termID), student)
	expectStatus(t, w, http.StatusConflict)
	if !jsonHasCode(t, w.Body.Bytes(), "review_conflict") {
		t.Fatalf("ждали review_conflict: %s", w.Body.String())
	}
	// Новая оценка до следующего показа (вторая вкладка) — 409 not_due
	w = securityRequest(h, "POST", "/reviews",
		fmt.Sprintf(`{"review_id":%q,"term_id":%d,"rating":"easy"}`, reviewID(2), termID), student)
	expectStatus(t, w, http.StatusConflict)
	if !jsonHasCode(t, w.Body.Bytes(), "not_due") {
		t.Fatalf("ждали not_due: %s", w.Body.String())
	}

	clock.set(first.Card.DueAt)
	second := postReview(t, h, student, reviewID(2), termID, "good", http.StatusCreated)
	if second.Card.State != srs.StateReview || second.Card.ScheduledDays < 1 || second.Card.Reps != 2 ||
		!second.Card.DueAt.Equal(first.Card.DueAt.Add(time.Duration(second.Card.ScheduledDays)*24*time.Hour)) ||
		second.Status != srs.Status(srs.Card{State: srs.StateReview, ScheduledDays: second.Card.ScheduledDays}) {
		t.Fatalf("второй good — выпуск в review: %+v", second)
	}
	clock.set(second.Card.DueAt)
	lapse := postReview(t, h, student, reviewID(3), termID, "again", http.StatusCreated)
	if lapse.Card.State != srs.StateRelearning || lapse.Card.Lapses != 1 || lapse.Status != 2 ||
		!lapse.Card.DueAt.Equal(second.Card.DueAt.Add(10*time.Minute)) {
		t.Fatalf("again в review — переобучение: %+v", lapse)
	}
	if _, reviews := srsCardCount(t, conn, studentID, termID); reviews != 3 {
		t.Fatalf("оценок в журнале: %d", reviews)
	}

	// Чужая карточка недостижима: у другого студента её нет — 404, карточка
	// владельца не тронута; тот же review_id у другого пользователя — свой
	expectStatus(t, securityRequest(h, "POST", "/reviews",
		fmt.Sprintf(`{"review_id":%q,"term_id":%d,"rating":"easy"}`, reviewID(4), termID), other), http.StatusNotFound)
	otherTerm := addWord(t, a, otherID, "apple")
	if otherTerm != termID {
		t.Fatalf("термин один на лемму: %d и %d", otherTerm, termID)
	}
	postReview(t, h, other, reviewID(1), termID, "easy", http.StatusCreated)
	if q := fetchQueue(t, h, student, ""); q.DueTotal != 0 {
		t.Fatalf("оценка другого студента сдвинула чужой график: %+v", q)
	}
	if _, reviews := srsCardCount(t, conn, studentID, termID); reviews != 3 {
		t.Fatalf("оценки владельца: %d", reviews)
	}

	expectStatus(t, securityRequest(h, "POST", "/reviews",
		fmt.Sprintf(`{"review_id":%q,"term_id":%d,"rating":"good"}`, reviewID(5), termID), teacher), http.StatusForbidden)
	expectStatus(t, securityRequest(h, "POST", "/reviews",
		fmt.Sprintf(`{"review_id":%q,"term_id":%d,"rating":"good"}`, reviewID(5), termID), nil), http.StatusUnauthorized)
	for _, body := range []string{
		fmt.Sprintf(`{"review_id":"not-a-uuid","term_id":%d,"rating":"good"}`, termID),
		fmt.Sprintf(`{"review_id":"00000000-0000-4000-8000-00000000000g","term_id":%d,"rating":"good"}`, termID),
		fmt.Sprintf(`{"review_id":%q,"term_id":0,"rating":"good"}`, reviewID(5)),
		fmt.Sprintf(`{"review_id":%q,"term_id":%d,"rating":"perfect"}`, reviewID(5), termID),
		fmt.Sprintf(`{"review_id":%q,"term_id":%d}`, reviewID(5), termID),
		`{"review_id":`,
	} {
		expectStatus(t, securityRequest(h, "POST", "/reviews", body, student), http.StatusBadRequest)
	}
	// Слово не в словаре — карточки нет
	stranger := ensureTerm(t, a, "river")
	expectStatus(t, securityRequest(h, "POST", "/reviews",
		fmt.Sprintf(`{"review_id":%q,"term_id":%d,"rating":"good"}`, reviewID(6), stranger), student), http.StatusNotFound)

	// Удаление слова уносит карточку и журнал
	expectStatus(t, securityRequest(h, "DELETE", fmt.Sprintf("/user-terms/%d", termID), "", student), http.StatusNoContent)
	if cards, reviews := srsCardCount(t, conn, studentID, termID); cards != 0 || reviews != 0 {
		t.Fatalf("после удаления слова: %d карточек, %d оценок", cards, reviews)
	}
}

func jsonHasCode(t *testing.T, body []byte, code string) bool {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatal(err)
	}
	return e.Code == code
}

// Один review_id из двух параллельных запросов засчитывается один раз:
// второй ждёт блокировку карточки и видит первый как повтор.
func TestReviewConcurrentDuplicate(t *testing.T) {
	a, conn := securityDB(t)
	useTestClock(a)
	studentID, student := securityUser(t, a, conn, store.RoleStudent, "racer")
	h := a.Router()
	termID := addWord(t, a, studentID, "apple")
	body := fmt.Sprintf(`{"review_id":%q,"term_id":%d,"rating":"hard"}`, reviewID(1), termID)
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Go(func() { codes[i] = securityRequest(h, "POST", "/reviews", body, student).Code })
	}
	wg.Wait()
	if codes[0]+codes[1] != http.StatusCreated+http.StatusOK || codes[0] == codes[1] {
		t.Fatalf("коды: %v", codes)
	}
	if _, reviews := srsCardCount(t, conn, studentID, termID); reviews != 1 {
		t.Fatalf("оценок: %d", reviews)
	}
}

func TestSRSMigrationReversible(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	studentID, _ := securityUser(t, a, conn, store.RoleStudent, "srs-migration")
	termID := addWord(t, a, studentID, "apple")
	tables := func() string {
		t.Helper()
		var present string
		if err := conn.QueryRow(ctx,
			`SELECT concat_ws(',', to_regclass('srs_cards'), to_regclass('srs_reviews'))`).Scan(&present); err != nil {
			t.Fatal(err)
		}
		return present
	}
	if got := tables(); got != "srs_cards,srs_reviews" {
		t.Fatalf("после наката: %q", got)
	}
	// Ответы cloze (0022) ссылаются на оценки: их откат — раньше, накат — после
	testdb.Apply(t, conn, "0022_cloze.down.sql")
	testdb.Apply(t, conn, "0021_srs.down.sql")
	if got := tables(); got != "" {
		t.Fatalf("откат оставил %q", got)
	}
	if st, err := a.store.UserTermStatus(ctx, studentID, termID); err != nil || st == nil {
		t.Fatalf("откат задел словарь: %v, %v", st, err)
	}
	testdb.Apply(t, conn, "0021_srs.up.sql")
	testdb.Apply(t, conn, "0022_cloze.up.sql")
	if got := tables(); got != "srs_cards,srs_reviews" {
		t.Fatalf("повторный накат: %q", got)
	}
	// Карточки старых слов после наката заводит бэкфилл worker'а
	if n, err := a.store.BackfillSRSCards(ctx); err != nil || n != 1 {
		t.Fatalf("бэкфилл после наката: %d, %v", n, err)
	}
}
