package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/llm"
	"github.com/IEZhu/class/backend/internal/llm/llmtest"
	"github.com/IEZhu/class/backend/internal/srs"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/tatoeba"
	"github.com/IEZhu/class/backend/internal/testdb"
)

// clozeFixture — api с провайдером LLM provider (nil — ключа нет) и
// суточным лимитом «объясни» limit, подставленные часы, преподаватель,
// урок Алисы и Кэрол и урок Боба.
type clozeFixture struct {
	a     *API
	conn  *pgx.Conn
	h     http.Handler
	clock *testClock

	teacherID, aliceID, bobID, carolID int64
	teacher, alice, bob, carol         *http.Cookie
	lessonID, bobLessonID              int64
}

func newClozeFixture(t *testing.T, provider llm.Provider, limit int) *clozeFixture {
	t.Helper()
	base, conn := securityDB(t)
	a, err := New(base.store, Config{LLM: provider, LLMExplainDailyLimit: limit})
	if err != nil {
		t.Fatal(err)
	}
	f := &clozeFixture{a: a, conn: conn, h: a.Router(), clock: useTestClock(a)}
	f.teacherID, f.teacher = securityUser(t, a, conn, store.RoleTeacher, "cloze-teacher")
	f.aliceID, f.alice = securityUser(t, a, conn, store.RoleStudent, "cloze-alice")
	f.bobID, f.bob = securityUser(t, a, conn, store.RoleStudent, "cloze-bob")
	f.carolID, f.carol = securityUser(t, a, conn, store.RoleStudent, "cloze-carol")
	f.lessonID = lessonWithStudent(t, a, conn, f.teacherID, "cloze group", f.aliceID, f.carolID)
	f.bobLessonID = lessonWithStudent(t, a, conn, f.teacherID, "bob group", f.bobID)
	return f
}

// transcript — реплики транскрипта урока, как их сохранил бы asr_result.
func (f *clozeFixture) transcript(t *testing.T, lessonID int64, texts ...string) {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := f.conn.QueryRow(ctx, `INSERT INTO transcripts (lesson_id, provider, status, external_id)
		VALUES ($1, 'assemblyai', 'completed', $2) RETURNING id`, lessonID, fmt.Sprintf("tr-%d", lessonID)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for i, text := range texts {
		if _, err := f.conn.Exec(ctx, `INSERT INTO utterances (transcript_id, idx, speaker_label, start_ms, end_ms, text)
			VALUES ($1, $2, 'A', $3, $4, $5)`, id, i, i*1000, i*1000+900, text); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *clozeFixture) word(t *testing.T, userID int64, word string, lessonID *int64) int64 {
	t.Helper()
	termID := ensureTerm(t, f.a, word)
	if _, err := f.a.store.SaveUserTerm(context.Background(), userID, termID, nil, lessonID); err != nil {
		t.Fatal(err)
	}
	return termID
}

func (f *clozeFixture) get(t *testing.T, cookie *http.Cookie) (clozeResponse, string) {
	t.Helper()
	w := securityRequest(f.h, "GET", "/practice/cloze", "", cookie)
	expectStatus(t, w, http.StatusOK)
	var resp clozeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp, w.Body.String()
}

func attemptBody(n int, answer, mode string, ms int) string {
	return fmt.Sprintf(`{"attempt_id":%q,"answer":%q,"mode":%q,"response_ms":%d}`, reviewID(n), answer, mode, ms)
}

func clozeAttemptPath(itemID int64) string { return fmt.Sprintf("/practice/cloze/%d/attempts", itemID) }

func (f *clozeFixture) attempt(t *testing.T, cookie *http.Cookie, itemID int64, body string, want int) clozeAttemptResponse {
	t.Helper()
	w := securityRequest(f.h, "POST", clozeAttemptPath(itemID), body, cookie)
	expectStatus(t, w, want)
	var resp clozeAttemptResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// itemTerm — слово пункта (в ответе выдачи его нет).
func (f *clozeFixture) itemTerm(t *testing.T, itemID int64) int64 {
	t.Helper()
	var termID int64
	if err := f.conn.QueryRow(context.Background(), `SELECT term_id FROM cloze_items WHERE id = $1`, itemID).Scan(&termID); err != nil {
		t.Fatal(err)
	}
	return termID
}

// itemOf — пункт выдачи по слову.
func (f *clozeFixture) itemOf(t *testing.T, resp clozeResponse, termID int64) clozeItemResponse {
	t.Helper()
	for _, it := range resp.Items {
		if f.itemTerm(t, it.ID) == termID {
			return it
		}
	}
	t.Fatalf("в выдаче нет пункта слова %d: %+v", termID, resp.Items)
	return clozeItemResponse{}
}

func cardOf(t *testing.T, conn *pgx.Conn, userID, termID int64) srs.Card {
	t.Helper()
	var c srs.Card
	if err := conn.QueryRow(context.Background(), `SELECT due_at, stability, difficulty, scheduled_days, reps, lapses,
		    state, remaining_steps, last_review_at FROM srs_cards WHERE user_id = $1 AND term_id = $2`, userID, termID).
		Scan(&c.Due, &c.Stability, &c.Difficulty, &c.ScheduledDays, &c.Reps, &c.Lapses, &c.State,
			&c.RemainingSteps, &c.LastReview); err != nil {
		t.Fatal(err)
	}
	return c
}

// DoD S4-4: по словарю урока (слова назначены с уроком-источником тем же
// SaveUserTerm, что и S3-8) выдача — валидный тест из пяти пунктов:
// предложения из реплик урока, пропуск на месте слова, четыре варианта
// с ответом, дистракторы — слова того же урока.
func TestClozeLessonDictionaryGivesTest(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	f.transcript(t, f.lessonID,
		"Good morning. I eat an apple every morning before school.",
		"We walked along the river after dinner.",
		"My grandmother grows tomatoes in her garden.",
		"Please close the window because it is cold.",
		"Our teacher gave us a lot of homework.")
	lessonWords := []string{"apple", "river", "garden", "window", "teacher"}
	for _, w := range lessonWords {
		f.word(t, f.aliceID, w, &f.lessonID)
	}
	resp, raw := f.get(t, f.alice)
	if len(resp.Items) != 5 || resp.DueTotal != 5 || resp.LLMAvailable {
		t.Fatalf("выдача: %d пунктов, к показу %d, llm %v", len(resp.Items), resp.DueTotal, resp.LLMAvailable)
	}
	// Ответ не уходит клиенту до ответа: ни слова, ни термина, ни перевода
	for _, field := range []string{`"lemma"`, `"term_id"`, `"translation"`, `"answer"`, `"sentence"`} {
		if strings.Contains(raw, field) {
			t.Fatalf("в выдаче поле %s: %s", field, raw)
		}
	}
	for _, it := range resp.Items {
		answer := lessonWords[slices.IndexFunc(lessonWords, func(w string) bool { return f.itemTerm(t, it.ID) == ensureTerm(t, f.a, w) })]
		if it.Source != store.ClozeSourceLesson || it.LessonID == nil || *it.LessonID != f.lessonID || !it.Due ||
			it.DistractorSource != distractorsList || it.Attribution != nil {
			t.Errorf("пункт %q: %+v", answer, it)
		}
		if strings.Contains(it.Before+it.After, answer) || it.Before+answer+it.After == "" {
			t.Errorf("пункт %q: ответ в тексте %q ___ %q", answer, it.Before, it.After)
		}
		if len(it.Options) != 4 || !slices.Contains(it.Options, answer) {
			t.Errorf("пункт %q: варианты %q", answer, it.Options)
		}
		for _, o := range it.Options {
			if o != answer && !slices.Contains(lessonWords, o) {
				t.Errorf("пункт %q: дистрактор %q не из слов урока %q", answer, o, it.Options)
			}
		}
	}
	apple := f.itemOf(t, resp, ensureTerm(t, f.a, "apple"))
	if apple.Before != "I eat an " || apple.After != " every morning before school." {
		t.Fatalf("пропуск apple: %q ___ %q", apple.Before, apple.After)
	}
	// Повтор выдачи — те же пункты с теми же вариантами в том же порядке
	again, _ := f.get(t, f.alice)
	if !reflect.DeepEqual(again.Items, resp.Items) {
		t.Fatalf("повтор выдачи другой:\n%+v\n%+v", again.Items, resp.Items)
	}
	if n := countRows(t, f.conn, `SELECT count(*) FROM cloze_items WHERE user_id = $1`, f.aliceID); n != 5 {
		t.Fatalf("пунктов в базе %d", n)
	}
}

// Выдача — только по своим словам и своим текстам: реплики своих уроков
// (урок, с которого пришло слово, раньше более нового), материалы, затем
// Tatoeba; урок Боба Алисе не виден, урок Алисы — Бобу. Отвеченный пункт сменяется новым
// предложением слова; когда они кончились, возвращается пункт, на
// который отвечали давнее всех.
func TestClozeOwnWordsAndSources(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	ctx := context.Background()
	importTatoeba(t, f.a, readTatoebaFixture(t).sources("v1"), tatoeba.Options{})
	f.transcript(t, f.lessonID, "Hello everyone. Yesterday I ran to the shop with my sister.")
	f.transcript(t, f.bobLessonID, "Bob likes to run along the river every day.")
	newer := lessonWithStudent(t, f.a, f.conn, f.teacherID, "second group", f.aliceID)
	f.transcript(t, newer, "They run to school when the bus is late.")
	if _, err := f.a.store.CreateMaterial(ctx, f.lessonID, "material", "Words",
		"# Words\n\n- We run in the park on Sundays.\n- [Video](https://example.com) run", nil); err != nil {
		t.Fatal(err)
	}
	run := f.word(t, f.aliceID, "run", &f.lessonID)
	f.word(t, f.bobID, "run", &f.bobLessonID)
	bank := f.word(t, f.bobID, "bank", nil)

	bobResp, _ := f.get(t, f.bob)
	if len(bobResp.Items) != 2 {
		t.Fatalf("у Боба пунктов %d", len(bobResp.Items))
	}
	if it := f.itemOf(t, bobResp, run); it.Source != store.ClozeSourceLesson || *it.LessonID != f.bobLessonID {
		t.Fatalf("пункт Боба: %+v", it)
	}
	if it := f.itemOf(t, bobResp, bank); it.Source != store.ClozeSourceTatoeba || it.Attribution == nil ||
		it.Attribution.Author != "fixture_bank" || it.Attribution.License != tatoeba.License {
		t.Fatalf("пункт Tatoeba: %+v", it)
	}

	var sources, sentences []string
	seen := map[int64]bool{}
	for i := 1; ; i++ {
		resp, _ := f.get(t, f.alice)
		if len(resp.Items) != 1 {
			t.Fatalf("у Алисы пунктов %d: только её слово", len(resp.Items))
		}
		it := resp.Items[0]
		if seen[it.ID] {
			// Предложения кончились: давнее всех отвечали на первый
			if first := f.itemOf(t, resp, run); sources[0] != store.ClozeSourceLesson || first.Before != "Yesterday I " {
				t.Fatalf("после всех предложений: %+v", first)
			}
			break
		}
		seen[it.ID] = true
		sources = append(sources, it.Source)
		res := f.attempt(t, f.alice, it.ID, attemptBody(i, "run", "choice", 1000), http.StatusCreated)
		sentences = append(sentences, res.Sentence)
		// Первый ответ — по наступившей карточке, дальше показ впереди:
		// верный ответ в график не идёт
		if res.Counted != (i == 1) || !res.Correct || res.Rating != srs.Easy {
			t.Fatalf("ответ %d: %+v", i, res)
		}
		if (it.Source == store.ClozeSourceTatoeba) != (res.Translation != nil) {
			t.Fatalf("перевод предложения %q: %v", it.Source, res.Translation)
		}
		if i > 10 {
			t.Fatal("выдача не заканчивает предложения")
		}
	}
	want := []string{"lesson", "lesson", "material", "tatoeba", "tatoeba", "tatoeba"}
	if !slices.Equal(sources, want) {
		t.Fatalf("источники %q, хотели %q (предложения %q)", sources, want, sentences)
	}
	for _, s := range sentences {
		if strings.Contains(s, "Bob") || strings.Contains(s, "http") {
			t.Fatalf("предложение не из своих текстов: %q", s)
		}
	}
	if sentences[1] != "They run to school when the bus is late." || sentences[2] != "We run in the park on Sundays." {
		t.Fatalf("второй урок и материал: %q", sentences[1:3])
	}
}

// DoD S4-5: ответ двигает карточку тем же планировщиком и по тем же
// правилам, что POST /reviews (ADR-036): верно и быстро — easy, верно —
// good, неверно — again; оценка ложится в srs_reviews с source cloze,
// статус слова — по карточке. Верный ответ раньше показа только пишется
// в журнал, промах засчитывается и раньше показа (ADR-037).
func TestClozeAttemptMovesCard(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	ctx := context.Background()
	f.transcript(t, f.lessonID, "I eat an apple every morning before school.")
	apple := f.word(t, f.aliceID, "apple", &f.lessonID)
	resp, _ := f.get(t, f.alice)
	item := resp.Items[0].ID
	sched := srs.NewScheduler()
	reviews := func() int {
		return countRows(t, f.conn, `SELECT count(*) FROM srs_reviews WHERE user_id = $1 AND term_id = $2 AND source = 'cloze'`,
			f.aliceID, apple)
	}
	expect := func(res clozeAttemptResponse, before srs.Card, rating srs.Rating) srs.Card {
		t.Helper()
		next, err := sched.Review(before, rating, f.clock.now())
		if err != nil {
			t.Fatal(err)
		}
		if res.Rating != rating || !res.Counted || res.Card.State != next.State || !res.Card.DueAt.Equal(next.Due) ||
			res.Card.Reps != next.Reps || res.Card.Lapses != next.Lapses || res.Status != srs.Status(next) {
			t.Fatalf("ответ %+v, хотели %s → %+v", res, rating, next)
		}
		if st, err := f.a.store.UserTermStatus(ctx, f.aliceID, apple); err != nil || *st != srs.Status(next) {
			t.Fatalf("статус слова %v, %v", st, err)
		}
		return next
	}

	// Новая карточка к показу: верно и быстро — easy
	card := cardOf(t, f.conn, f.aliceID, apple)
	card = expect(f.attempt(t, f.alice, item, attemptBody(1, "apple", "choice", 1200), http.StatusCreated), card, srs.Easy)
	var rating, source string
	var reviewedAt time.Time
	if err := f.conn.QueryRow(ctx, `SELECT r.rating, r.source, r.reviewed_at FROM cloze_attempts a
		JOIN srs_reviews r ON r.id = a.review_id WHERE a.client_id = $1`, reviewID(1)).Scan(&rating, &source, &reviewedAt); err != nil {
		t.Fatal(err)
	}
	if rating != "easy" || source != srs.SourceCloze || !reviewedAt.Equal(f.clock.now()) {
		t.Fatalf("журнал FSRS: %s %s %v", rating, source, reviewedAt)
	}

	// Показ впереди: верный ответ — только в журнал cloze, график не тронут
	early := f.attempt(t, f.alice, item, attemptBody(2, "apple", "choice", 900), http.StatusCreated)
	if early.Counted || !early.Correct || !early.Card.DueAt.Equal(card.Due) || reviews() != 1 {
		t.Fatalf("ранний верный ответ: %+v, оценок %d", early, reviews())
	}
	if n := countRows(t, f.conn, `SELECT count(*) FROM cloze_attempts WHERE client_id = $1 AND review_id IS NULL`, reviewID(2)); n != 1 {
		t.Fatal("ранний ответ не записан в журнал cloze")
	}
	// Промах засчитывается и до показа: карточка уходит в переобучение
	f.clock.set(f.clock.now().Add(time.Hour))
	miss := f.attempt(t, f.alice, item, attemptBody(3, "banana", "typing", 3000), http.StatusCreated)
	if miss.Correct || miss.Answer != "apple" || miss.Lemma != "apple" || miss.TermID != apple {
		t.Fatalf("промах: %+v", miss)
	}
	card = expect(miss, card, srs.Again)
	if card.State != srs.StateRelearning || card.Lapses != 1 || miss.Status != 2 {
		t.Fatalf("после промаха %+v", card)
	}
	// Показ наступил: верно, но медленно — good; ввод другой формы — верно, не точно
	f.clock.set(card.Due.Add(time.Second))
	slow := f.attempt(t, f.alice, item, attemptBody(4, "apples", "typing", 9000), http.StatusCreated)
	if !slow.Correct || slow.Exact {
		t.Fatalf("другая форма: %+v", slow)
	}
	card = expect(slow, card, srs.Good)
	// Ввод быстрее порога ввода (но медленнее порога выбора) — easy
	f.clock.set(card.Due.Add(time.Second))
	expect(f.attempt(t, f.alice, item, attemptBody(5, "Apple", "typing", 6000), http.StatusCreated), card, srs.Easy)
	if reviews() != 4 {
		t.Fatalf("оценок cloze %d", reviews())
	}
}

// Повтор attempt_id засчитывается один раз, занятый другим ответом — 409;
// ключ свой у каждого пользователя. Чужой пункт недостижим (404), гость
// без входа — 401, не студент — 403; кривой запрос — 400; слово убрали —
// пункты ушли вместе с ним.
func TestClozeAttemptIdempotencyAndAccess(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	f.transcript(t, f.lessonID, "I eat an apple every morning before school.")
	f.transcript(t, f.bobLessonID, "Bob ate a pear in the garden yesterday.")
	apple := f.word(t, f.aliceID, "apple", &f.lessonID)
	f.word(t, f.bobID, "pear", &f.bobLessonID)
	aliceResp, _ := f.get(t, f.alice)
	bobResp, _ := f.get(t, f.bob)
	item, bobItem := aliceResp.Items[0].ID, bobResp.Items[0].ID

	first := f.attempt(t, f.alice, item, attemptBody(1, "apple", "choice", 1000), http.StatusCreated)
	dup := f.attempt(t, f.alice, item, attemptBody(1, "apple", "choice", 1000), http.StatusOK)
	if !dup.Duplicate || dup.Rating != first.Rating || !dup.Counted || !dup.Card.DueAt.Equal(first.Card.DueAt) {
		t.Fatalf("повтор: %+v", dup)
	}
	w := securityRequest(f.h, "POST", clozeAttemptPath(item), attemptBody(1, "pear", "choice", 1000), f.alice)
	expectStatus(t, w, http.StatusConflict)
	if !strings.Contains(w.Body.String(), `"code":"attempt_conflict"`) {
		t.Fatalf("конфликт ключа: %s", w.Body.String())
	}
	if n := countRows(t, f.conn, `SELECT count(*) FROM srs_reviews WHERE user_id = $1`, f.aliceID); n != 1 {
		t.Fatalf("оценок %d после повторов", n)
	}
	// Тот же ключ у Боба — его собственная попытка
	f.attempt(t, f.bob, bobItem, attemptBody(1, "pear", "choice", 1000), http.StatusCreated)

	// Чужой пункт: ни ответа, ни LLM-дистракторов
	expectStatus(t, securityRequest(f.h, "POST", clozeAttemptPath(item), attemptBody(2, "apple", "choice", 1000), f.bob),
		http.StatusNotFound)
	expectStatus(t, securityRequest(f.h, "POST", clozeAttemptPath(999999), attemptBody(3, "apple", "choice", 1000), f.alice),
		http.StatusNotFound)
	for _, req := range []struct{ method, path, body string }{
		{"GET", "/practice/cloze", ""},
		{"POST", clozeAttemptPath(item), attemptBody(4, "apple", "choice", 1000)},
		{"POST", "/practice/cloze/distractors", fmt.Sprintf(`{"item_ids":[%d]}`, item)},
	} {
		expectStatus(t, securityRequest(f.h, req.method, req.path, req.body, nil), http.StatusUnauthorized)
		expectStatus(t, securityRequest(f.h, req.method, req.path, req.body, f.teacher), http.StatusForbidden)
	}
	for _, body := range []string{
		`{"attempt_id":"nope","answer":"apple","mode":"choice","response_ms":1}`,
		attemptBody(5, " ", "choice", 1),
		attemptBody(5, strings.Repeat("a", 65), "choice", 1),
		attemptBody(5, "apple", "voice", 1),
		attemptBody(5, "apple", "choice", -1),
		attemptBody(5, "apple", "choice", 600001),
		fmt.Sprintf(`{"attempt_id":%q,"answer":"apple","mode":"choice"}`, reviewID(5)),
		`{"attempt_id":`,
	} {
		expectStatus(t, securityRequest(f.h, "POST", clozeAttemptPath(item), body, f.alice), http.StatusBadRequest)
	}
	for _, q := range []string{"?limit=0", "?limit=21", "?limit=x"} {
		expectStatus(t, securityRequest(f.h, "GET", "/practice/cloze"+q, "", f.alice), http.StatusBadRequest)
	}

	expectStatus(t, securityRequest(f.h, "DELETE", fmt.Sprintf("/user-terms/%d", apple), "", f.alice), http.StatusNoContent)
	expectStatus(t, securityRequest(f.h, "POST", clozeAttemptPath(item), attemptBody(6, "apple", "choice", 1000), f.alice),
		http.StatusNotFound)
	if n := countRows(t, f.conn, `SELECT count(*) FROM cloze_items WHERE user_id = $1`, f.aliceID); n != 0 {
		t.Fatalf("пункты пережили удаление слова: %d", n)
	}
	if resp, _ := f.get(t, f.alice); len(resp.Items) != 0 {
		t.Fatalf("выдача без слов: %+v", resp.Items)
	}
}

// Без ключа LLM cloze работает целиком на дистракторах словаря:
// выдача и ответы без 503, llm_available — false; сами LLM-дистракторы
// отвечают 503, лимит не тратится.
func TestClozeWithoutLLMKey(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	f.transcript(t, f.lessonID, "I eat an apple every morning before school.")
	f.word(t, f.aliceID, "apple", &f.lessonID)
	resp, _ := f.get(t, f.alice)
	if resp.LLMAvailable || len(resp.Items) != 1 || resp.Items[0].DistractorSource != distractorsList {
		t.Fatalf("выдача без ключа: %+v", resp)
	}
	f.attempt(t, f.alice, resp.Items[0].ID, attemptBody(1, "apple", "choice", 1000), http.StatusCreated)
	w := securityRequest(f.h, "POST", "/practice/cloze/distractors", fmt.Sprintf(`{"item_ids":[%d]}`, resp.Items[0].ID), f.alice)
	expectStatus(t, w, http.StatusServiceUnavailable)
	if !strings.Contains(w.Body.String(), `"code":"llm_not_configured"`) {
		t.Fatalf("без ключа: %s", w.Body.String())
	}
	if n := countRows(t, f.conn, `SELECT count(*) FROM llm_usage`); n != 0 {
		t.Fatalf("лимит потрачен без ключа: %d", n)
	}
}

func postDistractors(t *testing.T, f *clozeFixture, cookie *http.Cookie, want int, ids ...int64) clozeDistractorsResponse {
	t.Helper()
	b, err := json.Marshal(clozeDistractorsRequest{ItemIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	w := securityRequest(f.h, "POST", "/practice/cloze/distractors", string(b), cookie)
	expectStatus(t, w, want)
	var resp clozeDistractorsResponse
	if want == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
	}
	return resp
}

// S5-8, часть 2: LLM-дистракторы на фейке провайдера — один вызов на
// пункты без кэша, пункты данными; негодный ответ пункта оставляет
// дистракторы словаря; годные кэшируются для всех (у Кэрол тот же пункт
// сразу с вариантами LLM), повтор не зовёт провайдера и не тратит лимит;
// новый вызов сверх суточного лимита — 429; сбой провайдера — 503.
func TestClozeLLMDistractors(t *testing.T) {
	fake := llmtest.Texts(`{"items":[{"id":"1","options":["walked","drove","flew"]},{"id":"2","options":["apple","apples","pear"]}]}`)
	f := newClozeFixture(t, fake, 1)
	f.transcript(t, f.lessonID,
		"Yesterday I ran to the shop with my sister.",
		"I eat an apple every morning before school.",
		"Our teacher gave us a lot of homework.")
	run := f.word(t, f.aliceID, "run", &f.lessonID)
	apple := f.word(t, f.aliceID, "apple", &f.lessonID)
	teacherWord := f.word(t, f.aliceID, "teacher", &f.lessonID)
	resp, _ := f.get(t, f.alice)
	if !resp.LLMAvailable {
		t.Fatal("llm_available при ключе")
	}
	runItem, appleItem := f.itemOf(t, resp, run).ID, f.itemOf(t, resp, apple).ID
	teacherItem := f.itemOf(t, resp, teacherWord).ID

	got := postDistractors(t, f, f.alice, http.StatusOK, runItem, appleItem)
	if len(got.Items) != 2 || got.Items[0].ID != runItem || got.Items[0].DistractorSource != distractorsLLM ||
		!slices.Equal(sorted(got.Items[0].Options), []string{"drove", "flew", "ran", "walked"}) {
		t.Fatalf("варианты LLM: %+v", got.Items)
	}
	// У apple модель повторила ответ и его форму — остались дистракторы словаря
	if got.Items[1].DistractorSource != distractorsList || !slices.Contains(got.Items[1].Options, "apple") {
		t.Fatalf("негодный ответ модели: %+v", got.Items[1])
	}
	reqs := fake.Requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0].Prompt, `"sentence":"Yesterday I ___ to the shop with my sister.","answer":"ran"`) ||
		!strings.HasPrefix(reqs[0].Prompt, "<items>") {
		t.Fatalf("запросы к LLM: %+v", reqs)
	}
	if n := countRows(t, f.conn, `SELECT count(*) FROM cloze_distractors`); n != 1 {
		t.Fatalf("в кэше %d записей", n)
	}
	// Выдача берёт варианты из кэша; у Кэрол (тот же урок, то же
	// предложение) — тоже, без вызова
	resp, _ = f.get(t, f.alice)
	if it := f.itemOf(t, resp, run); it.DistractorSource != distractorsLLM {
		t.Fatalf("выдача после кэша: %+v", it)
	}
	f.word(t, f.carolID, "run", &f.lessonID)
	carolResp, _ := f.get(t, f.carol)
	if len(carolResp.Items) != 1 || carolResp.Items[0].DistractorSource != distractorsLLM ||
		!slices.Contains(carolResp.Items[0].Options, "walked") {
		t.Fatalf("общий кэш: %+v", carolResp.Items)
	}
	// Повтор по пункту из кэша — без вызова и без траты лимита
	postDistractors(t, f, f.alice, http.StatusOK, runItem)
	if len(fake.Requests()) != 1 || countRows(t, f.conn, `SELECT requests FROM llm_usage WHERE user_id = $1`, f.aliceID) != 1 {
		t.Fatal("кэш позвал провайдера или потратил лимит")
	}
	// Пункт без кэша сверх лимита — 429, провайдер не вызван
	postDistractors(t, f, f.alice, http.StatusTooManyRequests, teacherItem)
	if len(fake.Requests()) != 1 {
		t.Fatal("вызов сверх лимита")
	}
	// Чужой пункт — 404, даже если свой рядом
	postDistractors(t, f, f.bob, http.StatusNotFound, runItem)
	for _, body := range []string{`{"item_ids":[]}`, `{"item_ids":[1,1]}`, `{"item_ids":[0]}`, `{}`,
		fmt.Sprintf(`{"item_ids":[%s]}`, strings.TrimSuffix(strings.Repeat("1,", 21), ","))} {
		expectStatus(t, securityRequest(f.h, "POST", "/practice/cloze/distractors", body, f.alice), http.StatusBadRequest)
	}
	// Ответ на пункт с вариантами LLM: дистрактор неверен, форма из
	// предложения — верна и точна
	if res := f.attempt(t, f.alice, runItem, attemptBody(1, "walked", "choice", 1000), http.StatusCreated); res.Correct {
		t.Fatalf("дистрактор засчитан: %+v", res)
	}
	if res := f.attempt(t, f.alice, runItem, attemptBody(2, "ran", "choice", 1000), http.StatusCreated); !res.Correct || !res.Exact {
		t.Fatalf("ответ не засчитан: %+v", res)
	}

	// Сбой провайдера — 503 llm_unavailable, кэш не пишется
	failing, err := New(f.a.store, Config{LLM: llmtest.New(llmtest.Reply{Err: &llm.Error{Provider: llmtest.Name, Kind: llm.ErrUnavailable}})})
	if err != nil {
		t.Fatal(err)
	}
	w := securityRequest(failing.Router(), "POST", "/practice/cloze/distractors", fmt.Sprintf(`{"item_ids":[%d]}`, teacherItem), f.alice)
	expectStatus(t, w, http.StatusServiceUnavailable)
	if !strings.Contains(w.Body.String(), `"code":"llm_unavailable"`) || countRows(t, f.conn, `SELECT count(*) FROM cloze_distractors`) != 1 {
		t.Fatalf("сбой провайдера: %s", w.Body.String())
	}
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func TestClozeMigrationReversible(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	ctx := context.Background()
	f.transcript(t, f.lessonID, "I eat an apple every morning before school.")
	apple := f.word(t, f.aliceID, "apple", &f.lessonID)
	resp, _ := f.get(t, f.alice)
	f.attempt(t, f.alice, resp.Items[0].ID, attemptBody(1, "apple", "choice", 1000), http.StatusCreated)
	tables := func() string {
		t.Helper()
		var present string
		if err := f.conn.QueryRow(ctx, `SELECT concat_ws(',', to_regclass('cloze_items'), to_regclass('cloze_distractors'),
			to_regclass('cloze_attempts'))`).Scan(&present); err != nil {
			t.Fatal(err)
		}
		return present
	}
	if got := tables(); got != "cloze_items,cloze_distractors,cloze_attempts" {
		t.Fatalf("после наката: %q", got)
	}
	testdb.Apply(t, f.conn, "0022_cloze.down.sql")
	if got := tables(); got != "" {
		t.Fatalf("откат оставил %q", got)
	}
	// Оценка, которую успел записать ответ, — история графика FSRS
	if n := countRows(t, f.conn, `SELECT count(*) FROM srs_reviews WHERE user_id = $1 AND term_id = $2 AND source = 'cloze'`,
		f.aliceID, apple); n != 1 {
		t.Fatalf("откат задел журнал FSRS: %d", n)
	}
	testdb.Apply(t, f.conn, "0022_cloze.up.sql")
	if got := tables(); got != "cloze_items,cloze_distractors,cloze_attempts" {
		t.Fatalf("повторный накат: %q", got)
	}
	if resp, _ := f.get(t, f.alice); len(resp.Items) != 1 {
		t.Fatalf("выдача после наката: %+v", resp.Items)
	}
}
