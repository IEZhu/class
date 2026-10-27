package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/cloze"
	"github.com/IEZhu/class/backend/internal/lemma"
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
	return getCloze(t, f.h, cookie)
}

func getCloze(t *testing.T, h http.Handler, cookie *http.Cookie) (clozeResponse, string) {
	t.Helper()
	w := securityRequest(h, "GET", "/practice/cloze", "", cookie)
	expectStatus(t, w, http.StatusOK)
	var resp clozeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp, w.Body.String()
}

// generated — свой сгенерированный текст студента с леммами для поиска
// (как его сохранил бы POST /practice/generate-text).
func (f *clozeFixture) generated(t *testing.T, userID int64, body string, lemmas ...string) int64 {
	t.Helper()
	var id int64
	if err := f.conn.QueryRow(context.Background(), `INSERT INTO generated_texts (user_id, level, genre, max_words,
		    target_lemmas, lemmas, body, word_count, coverage, occurrences, unknown_share, attempts, provider, model)
		VALUES ($1, 'B1', 'story', 50, $2, $2, $3, 8, 1, '{}', 0, 1, 'fake', 'fake-model') RETURNING id`,
		userID, lemmas, body).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// sentenceOf — предложение пункта (в выдаче его нет).
func (f *clozeFixture) sentenceOf(t *testing.T, itemID int64) string {
	t.Helper()
	var s string
	if err := f.conn.QueryRow(context.Background(), `SELECT sentence FROM cloze_items WHERE id = $1`, itemID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// distinctSentences — у пунктов выдачи разные предложения.
func (f *clozeFixture) distinctSentences(t *testing.T, resp clozeResponse) {
	t.Helper()
	seen := map[string]int64{}
	for _, it := range resp.Items {
		s := f.sentenceOf(t, it.ID)
		if prev, dup := seen[s]; dup {
			t.Fatalf("пункты %d и %d из одного предложения %q", prev, it.ID, s)
		}
		seen[s] = it.ID
	}
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
// (урок, с которого пришло слово, раньше более нового), материалы, свои
// сгенерированные тексты, затем Tatoeba; урок и текст Боба Алисе не
// видны, урок Алисы — Бобу. Отвеченный пункт сменяется новым
// предложением слова; когда они кончились, возвращается пункт, на
// который отвечали давнее всех, — и при свежей записи «нет предложения»
// (у слова есть пункты, из очереди оно не выпадает). Ответ у вариантов
// словаря — лемма, а не форма из предложения (ran среди лемм выдал бы
// ответ). У Tatoeba до
// ответа — только автор и лицензия: ссылки на страницу предложения
// (там оно целиком) и перевод — в ответе на попытку.
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
	f.generated(t, f.aliceID, "My sister and I run before breakfast.", "my", "sister", "and", "i", "run", "before", "breakfast")
	f.generated(t, f.bobID, "Bob and his dog run every evening.", "bob", "and", "his", "dog", "run", "every", "evening")
	run := f.word(t, f.aliceID, "run", &f.lessonID)
	f.word(t, f.bobID, "run", &f.bobLessonID)
	bank := f.word(t, f.bobID, "bank", nil)

	bobResp, bobRaw := f.get(t, f.bob)
	if len(bobResp.Items) != 2 {
		t.Fatalf("у Боба пунктов %d", len(bobResp.Items))
	}
	if it := f.itemOf(t, bobResp, run); it.Source != store.ClozeSourceLesson || *it.LessonID != f.bobLessonID {
		t.Fatalf("пункт Боба: %+v", it)
	}
	bankItem := f.itemOf(t, bobResp, bank)
	if bankItem.Source != store.ClozeSourceTatoeba || bankItem.Attribution == nil ||
		bankItem.Attribution.Author != "fixture_bank" || bankItem.Attribution.License != tatoeba.License ||
		bankItem.Attribution.LicenseURL != tatoeba.LicenseURL {
		t.Fatalf("пункт Tatoeba: %+v", bankItem)
	}
	if strings.Contains(bobRaw, "tatoeba.org/sentences") || strings.Contains(bobRaw, `"url"`) {
		t.Fatalf("ссылка на предложение до ответа: %s", bobRaw)
	}
	bankRes := f.attempt(t, f.bob, bankItem.ID, attemptBody(1, "bank", "choice", 1000), http.StatusCreated)
	if a := bankRes.Attribution; a == nil || a.Author != "fixture_bank" || a.URL != tatoeba.SentenceURL(5) ||
		a.TranslationURL != tatoeba.SentenceURL(106) || a.License != tatoeba.License || bankRes.Translation == nil {
		t.Fatalf("подпись после ответа: %+v", bankRes)
	}

	var sources, sentences []string
	seen := map[int64]bool{}
	for i := 1; ; i++ {
		resp, _ := f.get(t, f.alice)
		if len(resp.Items) != 1 {
			t.Fatalf("у Алисы пунктов %d: только её слово", len(resp.Items))
		}
		it := resp.Items[0]
		if i == 1 && (it.Before != "Yesterday I " || !slices.Contains(it.Options, "run") || slices.Contains(it.Options, "ran")) {
			t.Fatalf("варианты словаря: ответ — лемма, а не форма: %+v", it)
		}
		if seen[it.ID] {
			// Предложения кончились: давнее всех отвечали на первый
			if first := f.itemOf(t, resp, run); sources[0] != store.ClozeSourceLesson || first.Before != "Yesterday I " {
				t.Fatalf("после всех предложений: %+v", first)
			}
			// Запись «нет предложения» теперь свежая, но у слова есть
			// пункты: оно остаётся в очереди, и следующая выдача — тот же
			// давний пункт
			if again, _ := f.get(t, f.alice); len(again.Items) != 1 || again.Items[0].ID != it.ID {
				t.Fatalf("слово с пунктами и свежей записью выпало из очереди: %+v", again.Items)
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
		if (it.Source == store.ClozeSourceTatoeba) != (res.Translation != nil) || (res.Translation != nil) != (res.Attribution != nil) {
			t.Fatalf("перевод и подпись предложения %q: %+v", it.Source, res)
		}
		if i > 10 {
			t.Fatal("выдача не заканчивает предложения")
		}
	}
	want := []string{"lesson", "lesson", "material", "generated", "tatoeba", "tatoeba", "tatoeba"}
	if !slices.Equal(sources, want) {
		t.Fatalf("источники %q, хотели %q (предложения %q)", sources, want, sentences)
	}
	for _, s := range sentences {
		if strings.Contains(s, "Bob") || strings.Contains(s, "http") {
			t.Fatalf("предложение не из своих текстов: %q", s)
		}
	}
	if sentences[1] != "They run to school when the bus is late." || sentences[2] != "We run in the park on Sundays." ||
		sentences[3] != "My sister and I run before breakfast." {
		t.Fatalf("второй урок, материал и свой текст: %q", sentences[1:4])
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

// Повтор attempt_id засчитывается один раз; занятый другим ответом —
// другим словом, другим пунктом или в другом режиме — 409; ключ свой
// у каждого пользователя. Чужой пункт недостижим (404), гость без входа —
// 401, не студент — 403; кривой запрос — 400; слово убрали — пункты ушли
// вместе с ним.
func TestClozeAttemptIdempotencyAndAccess(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	f.transcript(t, f.lessonID, "I eat an apple every morning before school.", "We walked along the river after dinner.")
	f.transcript(t, f.bobLessonID, "Bob ate a pear in the garden yesterday.")
	apple := f.word(t, f.aliceID, "apple", &f.lessonID)
	river := f.word(t, f.aliceID, "river", &f.lessonID)
	f.word(t, f.bobID, "pear", &f.bobLessonID)
	aliceResp, _ := f.get(t, f.alice)
	bobResp, _ := f.get(t, f.bob)
	item, riverItem, bobItem := f.itemOf(t, aliceResp, apple).ID, f.itemOf(t, aliceResp, river).ID, bobResp.Items[0].ID

	first := f.attempt(t, f.alice, item, attemptBody(1, "apple", "choice", 1000), http.StatusCreated)
	dup := f.attempt(t, f.alice, item, attemptBody(1, "apple", "choice", 1000), http.StatusOK)
	if !dup.Duplicate || dup.Rating != first.Rating || !dup.Counted || !dup.Card.DueAt.Equal(first.Card.DueAt) {
		t.Fatalf("повтор: %+v", dup)
	}
	for _, c := range []struct {
		name   string
		itemID int64
		body   string
	}{
		{"другой ответ", item, attemptBody(1, "pear", "choice", 1000)},
		{"другой пункт", riverItem, attemptBody(1, "apple", "choice", 1000)},
		{"другой режим", item, attemptBody(1, "apple", "typing", 1000)},
	} {
		w := securityRequest(f.h, "POST", clozeAttemptPath(c.itemID), c.body, f.alice)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"attempt_conflict"`) {
			t.Fatalf("конфликт ключа (%s): %d %s", c.name, w.Code, w.Body.String())
		}
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
	if n := countRows(t, f.conn, `SELECT count(*) FROM cloze_items WHERE user_id = $1 AND term_id = $2`, f.aliceID, apple); n != 0 {
		t.Fatalf("пункты пережили удаление слова: %d", n)
	}
	if resp, _ := f.get(t, f.alice); len(resp.Items) != 1 || resp.Items[0].ID != riverItem {
		t.Fatalf("выдача без слова: %+v", resp.Items)
	}
}

// Без ключа LLM cloze работает целиком на дистракторах словаря:
// выдача и ответы без 503, llm_available — false, у каждого пункта сразу
// четыре варианта (pending не бывает — странице нечего просить); сами
// LLM-дистракторы отвечают 503, лимит не тратится.
func TestClozeWithoutLLMKey(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	f.transcript(t, f.lessonID, "I eat an apple every morning before school.", "We walked along the river after dinner.",
		"My grandmother grows tomatoes in her garden.")
	for _, w := range []string{"apple", "river", "garden"} {
		f.word(t, f.aliceID, w, &f.lessonID)
	}
	resp, _ := f.get(t, f.alice)
	if resp.LLMAvailable || len(resp.Items) != 3 {
		t.Fatalf("выдача без ключа: %+v", resp)
	}
	for _, it := range resp.Items {
		if it.DistractorSource != distractorsList || len(it.Options) != 4 {
			t.Fatalf("пункт без ключа: %+v", it)
		}
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

func postDistractors(t *testing.T, h http.Handler, cookie *http.Cookie, want int, ids ...int64) clozeDistractorsResponse {
	t.Helper()
	b, err := json.Marshal(clozeDistractorsRequest{ItemIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	w := securityRequest(h, "POST", "/practice/cloze/distractors", string(b), cookie)
	expectStatus(t, w, want)
	var resp clozeDistractorsResponse
	if want == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
	}
	return resp
}

// optionSets — какой набор вариантов клиент уже получил по каждому
// пункту: второй, другой набор у того же пункта выдал бы ответ
// пересечением (ADR-037).
type optionSets struct {
	t    *testing.T
	seen map[int64][]string
}

func (o *optionSets) add(id int64, options []string, source string) {
	o.t.Helper()
	if (source == distractorsPending) != (len(options) == 0) {
		o.t.Fatalf("пункт %d: варианты %q при distractor_source %s", id, options, source)
	}
	if len(options) == 0 {
		return
	}
	if prev, ok := o.seen[id]; ok && !slices.Equal(prev, options) {
		o.t.Fatalf("пункт %d: второй набор вариантов %q после %q", id, options, prev)
	}
	o.seen[id] = options
}

func (o *optionSets) page(resp clozeResponse) {
	o.t.Helper()
	for _, it := range resp.Items {
		o.add(it.ID, it.Options, it.DistractorSource)
	}
}

func (o *optionSets) distractors(resp clozeDistractorsResponse) {
	o.t.Helper()
	for _, it := range resp.Items {
		if it.DistractorSource == distractorsPending {
			o.t.Fatalf("POST /practice/cloze/distractors оставил пункт без вариантов: %+v", it)
		}
		o.add(it.ID, it.Options, it.DistractorSource)
	}
}

func llmErrorOf(resp clozeDistractorsResponse) string {
	if resp.LLMError == nil {
		return ""
	}
	return *resp.LLMError
}

// S5-8, часть 2, на фейке провайдера, и один набор вариантов у пункта
// (ADR-037). С ключом выдача отдаёт варианты словаря только первому
// пункту (он сразу на экране) и закрепляет кэш LLM, если запись есть;
// остальные — pending. POST /practice/cloze/distractors отдаёт им
// окончательные варианты одним вызовом на пункты без кэша: проверенные
// ложатся в общий кэш (у Кэрол тот же пункт сразу с вариантами LLM),
// негодный ответ модели — словарь. Закреплённый набор не меняют ни
// повтор, ни новая выдача, и ни один пункт не получает двух разных
// наборов. Кэш лимит не тратит; лимит — счётчик «объясни» (kind
// explain, его тратит и само «объясни»): исчерпан — словарь с llm_error
// rate_limited, провайдер не зовётся; сбой провайдера — словарь
// с llm_unavailable, кэш не пишется.
func TestClozeLLMDistractors(t *testing.T) {
	ctx := context.Background()
	fake := llmtest.Texts(`{"items":[{"id":"1","options":["walked","drove","flew"]},{"id":"2","options":["apple","apples","pear"]}]}`)
	f := newClozeFixture(t, fake, 1)
	f.transcript(t, f.lessonID,
		"Our teacher gave us a lot of homework.",
		"Yesterday I ran to the shop with my sister.",
		"I eat an apple every morning before school.",
		"We swam in the river after dinner.")
	teacherWord := f.word(t, f.aliceID, "teacher", &f.lessonID)
	run := f.word(t, f.aliceID, "run", &f.lessonID)
	apple := f.word(t, f.aliceID, "apple", &f.lessonID)
	river := f.word(t, f.aliceID, "river", &f.lessonID)
	sets := &optionSets{t: t, seen: map[int64][]string{}}

	resp, _ := f.get(t, f.alice)
	sets.page(resp)
	if !resp.LLMAvailable || len(resp.Items) != 4 {
		t.Fatalf("выдача с ключом: %+v", resp)
	}
	teacherItem, runItem := f.itemOf(t, resp, teacherWord), f.itemOf(t, resp, run).ID
	appleItem, riverItem := f.itemOf(t, resp, apple).ID, f.itemOf(t, resp, river).ID
	if resp.Items[0].ID != teacherItem.ID || teacherItem.DistractorSource != distractorsList {
		t.Fatalf("первый пункт — сразу с вариантами словаря: %+v", resp.Items)
	}
	for _, it := range resp.Items[1:] {
		if it.DistractorSource != distractorsPending || len(it.Options) != 0 {
			t.Fatalf("пункт после первого — pending: %+v", it)
		}
	}

	got := postDistractors(t, f.h, f.alice, http.StatusOK, runItem, appleItem)
	sets.distractors(got)
	if len(got.Items) != 2 || got.Items[0].ID != runItem || got.Items[0].DistractorSource != distractorsLLM ||
		!slices.Equal(sorted(got.Items[0].Options), []string{"drove", "flew", "ran", "walked"}) || got.LLMError != nil {
		t.Fatalf("варианты LLM: %+v", got)
	}
	// У apple модель повторила ответ и его форму — закреплены дистракторы словаря
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
	// Вызов потратил счётчик «объясни», и только его
	if n := countRows(t, f.conn, `SELECT coalesce(sum(requests), 0) FROM llm_usage WHERE user_id = $1 AND kind = 'explain'`, f.aliceID); n != 1 ||
		countRows(t, f.conn, `SELECT count(*) FROM llm_usage WHERE user_id = $1 AND kind <> 'explain'`, f.aliceID) != 0 {
		t.Fatalf("счётчик вызовов: explain %d", n)
	}
	// Закреплённые наборы — как есть: без вызова и без траты лимита
	again := postDistractors(t, f.h, f.alice, http.StatusOK, teacherItem.ID, runItem, appleItem)
	sets.distractors(again)
	if len(fake.Requests()) != 1 || again.LLMError != nil {
		t.Fatalf("повтор позвал провайдера: %+v", again)
	}
	// Пункт без кэша сверх лимита — словарь с причиной, провайдер не вызван
	limited := postDistractors(t, f.h, f.alice, http.StatusOK, riverItem)
	sets.distractors(limited)
	if len(fake.Requests()) != 1 || llmErrorOf(limited) != "rate_limited" || limited.Items[0].DistractorSource != distractorsList {
		t.Fatalf("сверх лимита: %+v", limited)
	}
	// Кэш, записанный позже чужой пачкой, закреплённый набор не меняет
	if _, err := f.conn.Exec(ctx, `INSERT INTO cloze_distractors (term_id, context_hash, options, provider, model)
		SELECT term_id, context_hash, ARRAY['swam', 'drove', 'flew'], 'fake', 'fake-model' FROM cloze_items WHERE id = $1`,
		riverItem); err != nil {
		t.Fatal(err)
	}
	// Новая выдача — те же наборы
	resp, _ = f.get(t, f.alice)
	sets.page(resp)
	for _, it := range resp.Items {
		if it.DistractorSource == distractorsPending {
			t.Fatalf("закреплённый пункт снова pending: %+v", it)
		}
	}
	if f.itemOf(t, resp, run).DistractorSource != distractorsLLM {
		t.Fatalf("выдача после кэша: %+v", resp.Items)
	}

	// Кэрол (тот же урок, то же предложение) получает варианты LLM из
	// кэша сразу в выдаче, без вызова
	f.word(t, f.carolID, "run", &f.lessonID)
	carolResp, _ := f.get(t, f.carol)
	sets.page(carolResp)
	if len(carolResp.Items) != 1 || carolResp.Items[0].DistractorSource != distractorsLLM ||
		!slices.Contains(carolResp.Items[0].Options, "walked") || len(fake.Requests()) != 1 {
		t.Fatalf("общий кэш: %+v", carolResp.Items)
	}

	// Счётчик общий с «объясни»: Боб потратил его объяснением — его пункт
	// без кэша получает словарь, провайдер не вызван
	f.transcript(t, f.bobLessonID, "Bob ate a pear after lunch.", "Bob works in the garden every weekend.")
	f.word(t, f.bobID, "pear", &f.bobLessonID)
	garden := f.word(t, f.bobID, "garden", &f.bobLessonID)
	if ok, err := f.a.store.ReserveLLMRequest(ctx, f.bobID, store.LLMKindExplain, 1); err != nil || !ok {
		t.Fatalf("объяснение Боба: %v %v", ok, err)
	}
	bobResp, _ := f.get(t, f.bob)
	sets.page(bobResp)
	gardenItem := f.itemOf(t, bobResp, garden).ID
	bobGot := postDistractors(t, f.h, f.bob, http.StatusOK, gardenItem)
	sets.distractors(bobGot)
	if llmErrorOf(bobGot) != "rate_limited" || len(fake.Requests()) != 1 {
		t.Fatalf("лимит Боба после «объясни»: %+v, вызовов %d", bobGot, len(fake.Requests()))
	}
	// Чужой пункт — 404, даже рядом со своим
	postDistractors(t, f.h, f.bob, http.StatusNotFound, gardenItem, runItem)
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

	// Сбой провайдера — словарь с llm_unavailable, кэш не пишется
	failing, err := New(f.a.store, Config{LLM: llmtest.New(llmtest.Reply{Err: &llm.Error{Provider: llmtest.Name, Kind: llm.ErrUnavailable}})})
	if err != nil {
		t.Fatal(err)
	}
	homework := f.word(t, f.carolID, "homework", &f.lessonID)
	carolResp, _ = getCloze(t, failing.Router(), f.carol)
	sets.page(carolResp)
	failed := postDistractors(t, failing.Router(), f.carol, http.StatusOK, f.itemOf(t, carolResp, homework).ID)
	sets.distractors(failed)
	if llmErrorOf(failed) != "llm_unavailable" || failed.Items[0].DistractorSource != distractorsList ||
		countRows(t, f.conn, `SELECT count(*) FROM cloze_distractors`) != 2 {
		t.Fatalf("сбой провайдера: %+v", failed)
	}
}

// Ключ термина — верный ответ кнопкой и вводом, даже когда Lemma уводит
// ключ в другое слово (клик по roses заводит термин rose, а Lemma(rose)
// — rise): ответ засчитан, оценка не again.
func TestClozeTermKeyIsCorrectAnswer(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	cases := []struct{ click, sentence string }{
		{"roses", "She bought red roses for her mother."},
		{"wounds", "The soldier had deep wounds on his arm."},
		{"founded", "They founded the school in the old town."},
		{"bores", "This long film really bores my little brother."},
		{"sawed", "He sawed the old branch off the tree."},
		{"felled", "They felled the tall tree near the river."},
		{"bettered", "She bettered her own record this year."},
	}
	keys := map[int64]string{}
	var texts []string
	for _, c := range cases {
		texts = append(texts, c.sentence)
		// Термин — как его заводит клик: ключ — лемма кликнутой формы
		keys[f.word(t, f.aliceID, c.click, &f.lessonID)] = lemma.Lemma(c.click)
	}
	f.transcript(t, f.lessonID, texts...)
	resp, _ := f.get(t, f.alice)
	if len(resp.Items) != len(cases) {
		t.Fatalf("пунктов %d из %d", len(resp.Items), len(cases))
	}
	n := 0
	for _, it := range resp.Items {
		key := keys[f.itemTerm(t, it.ID)]
		if !slices.Contains(it.Options, key) {
			t.Fatalf("термин %q: кнопки ответа нет среди %q", key, it.Options)
		}
		for _, mode := range []string{"choice", "typing"} {
			n++
			res := f.attempt(t, f.alice, it.ID, attemptBody(n, key, mode, 1000), http.StatusCreated)
			if !res.Correct || res.Rating == srs.Again {
				t.Errorf("термин %q (%s): %+v", key, mode, res)
			}
		}
	}
}

// Слова без годного предложения во главе очереди не съедают выдачу:
// 35 самых просроченных слов без текстов не мешают набрать десять пунктов
// по словам с предложениями. Слово без предложения запоминается
// (cloze_misses) и следующей выдачей не ищется, пока запись свежая; через
// час — ищется снова.
func TestClozeFullBatchBehindSentencelessWords(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	ctx := context.Background()
	var lonely []int64
	for i := range 35 {
		lonely = append(lonely, f.word(t, f.aliceID, fmt.Sprintf("zorbix%c%c", 'a'+i/26, 'a'+i%26), nil))
	}
	if _, err := f.conn.Exec(ctx, `UPDATE srs_cards SET due_at = now() - interval '60 days'
		WHERE user_id = $1 AND term_id = ANY($2)`, f.aliceID, lonely); err != nil {
		t.Fatal(err)
	}
	lines := map[string]string{
		"apple": "I eat an apple every day.", "river": "We swam in the river.", "garden": "Flowers grow in our garden.",
		"window": "Please close the window now.", "teacher": "Our teacher is very kind.", "kitchen": "Mum is cooking in the kitchen.",
		"bicycle": "He rides a bicycle to work.", "mountain": "They climbed a high mountain.",
		"library": "She reads books in the library.", "doctor": "The doctor visited my grandfather.",
		"camera": "My new camera takes good photos.",
	}
	var texts []string
	words := map[int64]bool{}
	for w, line := range lines {
		texts = append(texts, line)
		words[f.word(t, f.aliceID, w, &f.lessonID)] = true
	}
	f.transcript(t, f.lessonID, texts...)

	resp, _ := f.get(t, f.alice)
	if len(resp.Items) != defaultClozeLimit {
		t.Fatalf("пунктов %d, хотели полную выдачу %d", len(resp.Items), defaultClozeLimit)
	}
	for _, it := range resp.Items {
		if !words[f.itemTerm(t, it.ID)] {
			t.Fatalf("пункт по слову без предложения: %+v", it)
		}
	}
	checkedAt := f.clock.now()
	misses := func(at time.Time) int {
		return countRows(t, f.conn, `SELECT count(*) FROM cloze_misses WHERE user_id = $1 AND checked_at = $2`, f.aliceID, at)
	}
	if n := misses(checkedAt); n != len(lonely) {
		t.Fatalf("запомнено слов без предложения %d", n)
	}
	// Через минуту запись свежая: слова не ищутся снова, выдача та же
	f.clock.set(checkedAt.Add(time.Minute))
	if again, _ := f.get(t, f.alice); !reflect.DeepEqual(again.Items, resp.Items) || misses(checkedAt) != len(lonely) {
		t.Fatalf("повтор выдачи: %d пунктов, записей прежней проверки %d", len(again.Items), misses(checkedAt))
	}
	// Через час (ADR-037) — ищутся снова
	later := checkedAt.Add(time.Hour + time.Minute)
	f.clock.set(later)
	if again, _ := f.get(t, f.alice); len(again.Items) != defaultClozeLimit || misses(later) != len(lonely) {
		t.Fatalf("после часа: %d пунктов, перепроверено %d", len(again.Items), misses(later))
	}
}

// Запись «нет предложения» сбрасывают новые источники студента: свой
// сгенерированный текст, импорт Tatoeba и материал без файла в его уроке,
// появившиеся после проверки (ADR-037). Новый транскрипт — нет (у него
// нет времени готовности): реплика ждёт, пока запись устареет.
func TestClozeMissCacheNewSources(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	river := f.word(t, f.aliceID, "river", &f.lessonID)
	bank := f.word(t, f.aliceID, "bank", nil)
	// Часы api — настоящие: время проверки сравнимо со временем
	// источников (now() базы)
	f.clock.set(time.Now())
	if resp, _ := f.get(t, f.alice); len(resp.Items) != 0 {
		t.Fatalf("без текстов: %+v", resp.Items)
	}
	if n := countRows(t, f.conn, `SELECT count(*) FROM cloze_misses WHERE user_id = $1`, f.aliceID); n != 2 {
		t.Fatalf("запомнено %d слов", n)
	}
	f.transcript(t, f.lessonID, "We swam in the river after dinner.")
	if resp, _ := f.get(t, f.alice); len(resp.Items) != 0 {
		t.Fatalf("свежая запись, а реплику нашли: %+v", resp.Items)
	}
	f.generated(t, f.aliceID, "The river was cold and fast.", "the", "river", "be", "cold", "and", "fast")
	resp, _ := f.get(t, f.alice)
	if len(resp.Items) != 1 || f.itemTerm(t, resp.Items[0].ID) != river {
		t.Fatalf("после своего текста: %+v", resp.Items)
	}
	// Проверка bank — позже своего текста: запись снова свежая
	f.clock.set(time.Now())
	if resp, _ := f.get(t, f.alice); len(resp.Items) != 1 {
		t.Fatalf("до импорта Tatoeba: %+v", resp.Items)
	}
	importTatoeba(t, f.a, readTatoebaFixture(t).sources("v1"), tatoeba.Options{})
	resp, _ = f.get(t, f.alice)
	if len(resp.Items) != 2 || f.itemOf(t, resp, bank).Source != store.ClozeSourceTatoeba {
		t.Fatalf("после импорта Tatoeba: %+v", resp.Items)
	}
	// Проверка kitchen — позже всех источников; затем учитель добавляет
	// в урок материал без файла: его created_at сбрасывает запись
	kitchen := f.word(t, f.aliceID, "kitchen", &f.lessonID)
	f.clock.set(time.Now())
	for range 2 {
		if resp, _ := f.get(t, f.alice); len(resp.Items) != 2 {
			t.Fatalf("до материала: %+v", resp.Items)
		}
	}
	if n := countRows(t, f.conn, `SELECT count(*) FROM cloze_misses WHERE user_id = $1 AND term_id = $2`, f.aliceID, kitchen); n != 1 {
		t.Fatalf("kitchen не записано как слово без предложения: %d", n)
	}
	if _, err := f.a.store.CreateMaterial(context.Background(), f.lessonID, "material", "Dinner",
		"Mum is cooking dinner in the kitchen.", nil); err != nil {
		t.Fatal(err)
	}
	resp, _ = f.get(t, f.alice)
	if len(resp.Items) != 3 || f.itemOf(t, resp, kitchen).Source != store.ClozeSourceMaterial {
		t.Fatalf("после материала: %+v", resp.Items)
	}
}

// Предложение — не больше чем в одном пункте выдачи: итог первого ответа
// показывает его целиком. Слово, чьё единственное предложение уже
// в выдаче, ждёт следующей (это не «нет предложения»), а у слова, которому
// досталось чужое предложение, берётся другое.
func TestClozeBatchDistinctSentences(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	f.transcript(t, f.lessonID, "I usually run to the station before my train leaves.",
		"The station was crowded and noisy this morning.")
	run := f.word(t, f.aliceID, "run", &f.lessonID)
	station := f.word(t, f.aliceID, "station", &f.lessonID)
	train := f.word(t, f.aliceID, "train", &f.lessonID)

	resp, _ := f.get(t, f.alice)
	f.distinctSentences(t, resp)
	if len(resp.Items) != 2 || f.sentenceOf(t, f.itemOf(t, resp, station).ID) != "The station was crowded and noisy this morning." {
		t.Fatalf("первая выдача: %+v", resp.Items)
	}
	if n := countRows(t, f.conn, `SELECT count(*) FROM cloze_misses WHERE user_id = $1`, f.aliceID); n != 0 {
		t.Fatalf("слово с занятым предложением записано как слово без предложения: %d", n)
	}
	f.attempt(t, f.alice, f.itemOf(t, resp, run).ID, attemptBody(1, "run", "choice", 1000), http.StatusCreated)
	// run отвечен и ушёл вперёд по графику; train получает общее
	// предложение, и давний пункт run из него в выдачу уже не идёт
	resp, _ = f.get(t, f.alice)
	f.distinctSentences(t, resp)
	if len(resp.Items) != 2 || f.itemOf(t, resp, train).ID == 0 {
		t.Fatalf("вторая выдача: %+v", resp.Items)
	}
}

// Материал с незавершённой загрузкой файла участникам не виден
// (visibleMaterials) — и в cloze не идёт; подтверждённый — идёт: запись
// «нет предложения», сделанная после появления материала, подтверждение
// сбрасывает.
func TestClozePendingMaterialNotASource(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	ctx := context.Background()
	m, err := f.a.store.CreateMaterial(ctx, f.lessonID, "homework", "Homework",
		"Tomorrow we will discuss the secret elephant answers together.", &store.MaterialFile{
			S3Key: fmt.Sprintf("materials/%d/answers.pdf", f.lessonID), Name: "answers.pdf",
			ContentType: "application/pdf", Size: 10,
		})
	if err != nil {
		t.Fatal(err)
	}
	f.word(t, f.aliceID, "elephant", &f.lessonID)
	// Проверка — позже создания материала, но раньше подтверждения файла
	f.clock.set(time.Now())
	for range 2 {
		if resp, _ := f.get(t, f.alice); len(resp.Items) != 0 {
			t.Fatalf("материал без загруженного файла — источник: %+v", resp.Items)
		}
	}
	if _, err := f.a.store.ConfirmMaterialFile(ctx, m.ID, "etag-1"); err != nil {
		t.Fatal(err)
	}
	resp, _ := f.get(t, f.alice)
	if len(resp.Items) != 1 || resp.Items[0].Source != store.ClozeSourceMaterial {
		t.Fatalf("после подтверждения: %+v", resp.Items)
	}
}

// Потолок очереди одной выдачи — 200 слов (ADR-037), и слова, которым
// недавно не нашлось предложения, его не съедают. 210 самых просроченных
// слов без текстов впереди 11 слов с предложениями: первая выдача
// (limit=1) проверяет ровно 200 слов — 200 записей cloze_misses, пунктов
// нет; вторая (через минуту, записи свежие) этих 200 слов в очереди уже не
// видит, проверяет оставшиеся 10 и набирает полную выдачу.
func TestClozeScanCapSkipsFreshMisses(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	ctx := context.Background()
	var lonely []int64
	for i := range 210 {
		lonely = append(lonely, f.word(t, f.aliceID, fmt.Sprintf("zorbix%c%c", 'a'+i/26, 'a'+i%26), nil))
	}
	if _, err := f.conn.Exec(ctx, `UPDATE srs_cards SET due_at = now() - interval '60 days'
		WHERE user_id = $1 AND term_id = ANY($2)`, f.aliceID, lonely); err != nil {
		t.Fatal(err)
	}
	lines := map[string]string{
		"apple": "I eat an apple every day.", "river": "We swam in the river.", "garden": "Flowers grow in our garden.",
		"window": "Please close the window now.", "teacher": "Our teacher is very kind.", "kitchen": "Mum is cooking in the kitchen.",
		"bicycle": "He rides a bicycle to work.", "mountain": "They climbed a high mountain.",
		"library": "She reads books in the library.", "doctor": "The doctor visited my grandfather.",
		"camera": "My new camera takes good photos.",
	}
	var texts []string
	words := map[int64]bool{}
	for w, line := range lines {
		texts = append(texts, line)
		words[f.word(t, f.aliceID, w, &f.lessonID)] = true
	}
	f.transcript(t, f.lessonID, texts...)
	misses := func() int {
		return countRows(t, f.conn, `SELECT count(*) FROM cloze_misses WHERE user_id = $1`, f.aliceID)
	}

	w := securityRequest(f.h, "GET", "/practice/cloze?limit=1", "", f.alice)
	expectStatus(t, w, http.StatusOK)
	var first clozeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 0 || misses() != 200 {
		t.Fatalf("первая выдача: пунктов %d, проверено слов без предложения %d из 200", len(first.Items), misses())
	}
	f.clock.set(f.clock.now().Add(time.Minute))
	resp, _ := f.get(t, f.alice)
	if len(resp.Items) != defaultClozeLimit || misses() != len(lonely) {
		t.Fatalf("вторая выдача: пунктов %d, записей %d", len(resp.Items), misses())
	}
	for _, it := range resp.Items {
		if !words[f.itemTerm(t, it.ID)] {
			t.Fatalf("пункт по слову без предложения: %+v", it)
		}
	}
}

// cacheDistractors — запись общего кэша LLM-дистракторов слова word
// в предложении, как её положил бы POST /practice/cloze/distractors.
func (f *clozeFixture) cacheDistractors(t *testing.T, termID int64, sentence, word string, options ...string) {
	t.Helper()
	b, ok := cloze.Find(sentence, word)
	if !ok {
		t.Fatalf("в %q нет пропуска для %q", sentence, word)
	}
	if err := f.a.store.SaveClozeDistractors(context.Background(), termID, cloze.Hash(sentence, b), options, "fake", "fake-model"); err != nil {
		t.Fatal(err)
	}
}

// Запись кэша, за которой закреплён набор пункта, удалили (рецепт ADR-037
// для неуместного слова): кнопкой остался бы один ответ, поэтому такой
// пункт в выдачу не идёт — ни отвеченный (давний), ни неотвеченный. Его
// предложение у слова занято; слово с другим предложением получает новый
// пункт с четырьмя вариантами, слово без другого — ни одного.
func TestClozeLLMSetWithoutCacheNotServed(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	ctx := context.Background()
	ranLine, swamLine := "Yesterday I ran to the park with my sister.", "We swam in the river after dinner."
	f.transcript(t, f.lessonID, ranLine, swamLine)
	f.generated(t, f.aliceID, "They swim in the lake every summer.", "they", "swim", "in", "the", "lake", "every", "summer")
	run := f.word(t, f.aliceID, "run", &f.lessonID)
	swim := f.word(t, f.aliceID, "swim", &f.lessonID)
	f.cacheDistractors(t, run, ranLine, "run", "walked", "drove", "flew")
	f.cacheDistractors(t, swim, swamLine, "swim", "walked", "sat", "slept")

	resp, _ := f.get(t, f.alice)
	if len(resp.Items) != 2 {
		t.Fatalf("выдача: %+v", resp.Items)
	}
	runItem, swimItem := f.itemOf(t, resp, run), f.itemOf(t, resp, swim)
	for _, it := range []clozeItemResponse{runItem, swimItem} {
		if it.DistractorSource != distractorsLLM || len(it.Options) != 4 {
			t.Fatalf("набор из кэша: %+v", it)
		}
	}
	f.attempt(t, f.alice, runItem.ID, attemptBody(1, "ran", "choice", 1000), http.StatusCreated)
	if _, err := f.conn.Exec(ctx, `DELETE FROM cloze_distractors`); err != nil {
		t.Fatal(err)
	}

	resp, _ = f.get(t, f.alice)
	if len(resp.Items) != 1 {
		t.Fatalf("после удаления кэша: %+v", resp.Items)
	}
	it := resp.Items[0]
	if it.ID == runItem.ID || it.ID == swimItem.ID || f.itemTerm(t, it.ID) != swim || len(it.Options) != 4 ||
		f.sentenceOf(t, it.ID) != "They swim in the lake every summer." {
		t.Fatalf("после удаления кэша: %+v", it)
	}
}

// nonLetters — вариантов не из одних строчных латинских букв.
func nonLetters(options []string) int {
	n := 0
	for _, o := range options {
		if strings.ContainsFunc(o, func(r rune) bool { return r < 'a' || r > 'z' }) {
			n++
		}
	}
	return n
}

// Ответ с дефисом или диакритикой (t-shirt, café) среди вариантов LLM
// (строчные латинские буквы) был бы единственным непохожим: такие пункты
// и с ключом сразу получают варианты словаря, даже когда запись кэша LLM
// есть, — и в выдаче, и в POST /practice/cloze/distractors; провайдер их
// не видит. Среди вариантов словаря у такого ответа есть второй непохожий
// (x-ray, t-shirt из словаря студента); у Боба такого слова нет — пункта
// t-shirt не будет. Обычное слово (run) по-прежнему получает варианты LLM.
func TestClozeOddShapedAnswersStayOffLLM(t *testing.T) {
	ctx := context.Background()
	fake := llmtest.Texts(`{"items":[{"id":"1","options":["walked","drove","flew"]}]}`)
	f := newClozeFixture(t, fake, 5)
	shirtLine, cafeLine := "He wore a white t-shirt to school today.", "We met at a small café near the station."
	f.transcript(t, f.lessonID, shirtLine, cafeLine, "Yesterday I ran to the park with my sister.")
	shirt := f.word(t, f.aliceID, "t-shirt", &f.lessonID)
	cafe := f.word(t, f.aliceID, "café", &f.lessonID)
	f.word(t, f.aliceID, "run", &f.lessonID)
	for _, w := range []string{"x-ray", "coat", "jacket", "sweater", "bar", "restaurant"} {
		f.word(t, f.aliceID, w, nil)
	}
	f.cacheDistractors(t, cafe, cafeLine, "café", "shop", "bar", "restaurant")
	sets := &optionSets{t: t, seen: map[int64][]string{}}
	odd := func(it clozeItemResponse) {
		t.Helper()
		if it.DistractorSource != distractorsList || len(it.Options) != 4 || nonLetters(it.Options) < 2 {
			t.Fatalf("пункт с непохожим ответом: %+v", it)
		}
	}

	resp, _ := f.get(t, f.alice)
	sets.page(resp)
	if len(resp.Items) != 3 || resp.Items[2].DistractorSource != distractorsPending {
		t.Fatalf("выдача: %+v", resp.Items)
	}
	shirtItem, cafeItem := f.itemOf(t, resp, shirt).ID, f.itemOf(t, resp, cafe).ID
	odd(f.itemOf(t, resp, shirt))
	odd(f.itemOf(t, resp, cafe))
	got := postDistractors(t, f.h, f.alice, http.StatusOK, shirtItem, cafeItem, resp.Items[2].ID)
	sets.distractors(got)
	if got.Items[2].DistractorSource != distractorsLLM || !slices.Contains(got.Items[2].Options, "ran") {
		t.Fatalf("обычное слово без вариантов LLM: %+v", got.Items[2])
	}
	// Пункт, которому набор ещё не отдавали, POST тоже не отправляет в LLM
	if _, err := f.conn.Exec(ctx, `UPDATE cloze_items SET option_source = NULL WHERE id = $1`, cafeItem); err != nil {
		t.Fatal(err)
	}
	got = postDistractors(t, f.h, f.alice, http.StatusOK, cafeItem)
	sets.distractors(got)
	odd(clozeItemResponse{ID: got.Items[0].ID, Options: got.Items[0].Options, DistractorSource: got.Items[0].DistractorSource})
	reqs := fake.Requests()
	if len(reqs) != 1 || strings.Contains(reqs[0].Prompt, "t-shirt") || strings.Contains(reqs[0].Prompt, "caf") {
		t.Fatalf("запросы к LLM: %+v", reqs)
	}

	f.transcript(t, f.bobLessonID, shirtLine)
	f.word(t, f.bobID, "t-shirt", &f.bobLessonID)
	for _, w := range []string{"coat", "jacket", "sweater"} {
		f.word(t, f.bobID, w, nil)
	}
	if bobResp, _ := f.get(t, f.bob); len(bobResp.Items) != 0 {
		t.Fatalf("t-shirt без второго непохожего слова: %+v", bobResp.Items)
	}
}

// Набор закрепляется только за пунктом без набора (option_source IS
// NULL): повторный LockClozeOptions с другим набором закреплённый не
// меняет, и выдача отдаёт прежние варианты — и list поверх llm, и llm
// поверх list при записи кэша.
func TestClozeLockKeepsFirstSet(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	ctx := context.Background()
	ranLine, swamLine := "Yesterday I ran to the park with my sister.", "We swam in the river after dinner."
	f.transcript(t, f.lessonID, ranLine, swamLine)
	run := f.word(t, f.aliceID, "run", &f.lessonID)
	swim := f.word(t, f.aliceID, "swim", &f.lessonID)
	f.cacheDistractors(t, run, ranLine, "run", "walked", "drove", "flew")
	resp, _ := f.get(t, f.alice)
	runItem, swimItem := f.itemOf(t, resp, run), f.itemOf(t, resp, swim)
	if runItem.DistractorSource != distractorsLLM || swimItem.DistractorSource != distractorsList {
		t.Fatalf("первая отдача: %+v", resp.Items)
	}
	f.cacheDistractors(t, swim, swamLine, "swim", "walked", "sat", "slept")
	if err := f.a.store.LockClozeOptions(ctx, f.aliceID, []int64{runItem.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.a.store.LockClozeOptions(ctx, f.aliceID, nil, []int64{swimItem.ID}); err != nil {
		t.Fatal(err)
	}
	again, _ := f.get(t, f.alice)
	if !reflect.DeepEqual(f.itemOf(t, again, run), runItem) || !reflect.DeepEqual(f.itemOf(t, again, swim), swimItem) {
		t.Fatalf("набор сменился: было %+v, стало %+v", resp.Items, again.Items)
	}
}

// Тот же attempt_id одновременно по двум словам: строки слов разные,
// блокировки друг друга не ждут, и запрос упирается в UNIQUE (user_id,
// client_id) — 409 attempt_conflict, его оценка FSRS откатывается.
// Чужая попытка держится открытой транзакцией, пока запрос не встанет
// на ключ.
func TestClozeAttemptKeyRace(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	ctx := context.Background()
	f.transcript(t, f.lessonID, "I eat an apple every morning before school.", "We walked along the river after dinner.")
	apple := f.word(t, f.aliceID, "apple", &f.lessonID)
	river := f.word(t, f.aliceID, "river", &f.lessonID)
	resp, _ := f.get(t, f.alice)
	appleItem, riverItem := f.itemOf(t, resp, apple).ID, f.itemOf(t, resp, river).ID

	tx, err := f.conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO cloze_attempts (item_id, user_id, client_id, mode, answer, correct, response_ms, rating)
		VALUES ($1, $2, $3, 'choice', 'river', true, 1000, 'easy')`, riverItem, f.aliceID, reviewID(1)); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- securityRequest(f.h, "POST", clozeAttemptPath(appleItem), attemptBody(1, "apple", "choice", 1000), f.alice)
	}()
	var w *httptest.ResponseRecorder
	deadline := time.Now().Add(10 * time.Second)
	for w == nil {
		select {
		case w = <-done: // не встал на ключ: проверка ниже упадёт
		default:
			var waiting int
			if err := f.a.store.Pool().QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE state = 'active' AND wait_event_type = 'Lock' AND query LIKE '%INSERT INTO cloze_attempts%'`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting > 0 {
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				w = <-done
			} else if time.Now().After(deadline) {
				t.Fatal("запрос не дошёл до вставки попытки")
			}
		}
	}
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"attempt_conflict"`) {
		t.Fatalf("гонка ключа: %d %s", w.Code, w.Body.String())
	}
	if n := countRows(t, f.conn, `SELECT count(*) FROM srs_reviews WHERE user_id = $1 AND term_id = $2`, f.aliceID, apple); n != 0 {
		t.Fatalf("оценка проигравшего запроса осталась: %d", n)
	}
}

// Пункты и их попытки уходят вместе с источником: удаление материала,
// отмена урока (реплики транскрипта), удаление своего текста и
// предложения Tatoeba при уборке импорта не падают на внешнем ключе.
func TestClozeSourceCascades(t *testing.T) {
	f := newClozeFixture(t, nil, 0)
	ctx := context.Background()
	importTatoeba(t, f.a, readTatoebaFixture(t).sources("v1"), tatoeba.Options{})
	m, err := f.a.store.CreateMaterial(ctx, f.lessonID, "material", "Walk", "We walked along the river after dinner.", nil)
	if err != nil {
		t.Fatal(err)
	}
	other := lessonWithStudent(t, f.a, f.conn, f.teacherID, "second group", f.aliceID)
	f.transcript(t, other, "I eat an apple every day at lunch.")
	f.generated(t, f.aliceID, "My grandmother grows tomatoes in her garden.", "my", "grandmother", "grow", "tomato", "in", "her", "garden")
	words := map[string]int64{}
	for _, w := range []string{"river", "apple", "garden", "bank"} {
		words[w] = f.word(t, f.aliceID, w, nil)
	}
	resp, _ := f.get(t, f.alice)
	if len(resp.Items) != 4 {
		t.Fatalf("пунктов %d", len(resp.Items))
	}
	for i, it := range resp.Items {
		f.attempt(t, f.alice, it.ID, attemptBody(i+1, "x", "typing", 1000), http.StatusCreated)
	}
	left := func(word string) int {
		return countRows(t, f.conn, `SELECT (SELECT count(*) FROM cloze_items WHERE user_id = $1 AND term_id = $2)
			+ (SELECT count(*) FROM cloze_attempts a JOIN cloze_items i ON i.id = a.item_id WHERE i.term_id = $2)`,
			f.aliceID, words[word])
	}
	for _, w := range []string{"river", "apple", "garden", "bank"} {
		if left(w) != 2 {
			t.Fatalf("у %s нет пункта с попыткой", w)
		}
	}
	expectStatus(t, securityRequest(f.h, "DELETE", fmt.Sprintf("/materials/%d", m.ID), "", f.teacher), http.StatusNoContent)
	expectStatus(t, securityRequest(f.h, "DELETE", fmt.Sprintf("/lessons/%d", other), "", f.teacher), http.StatusNoContent)
	for _, sql := range []string{
		`DELETE FROM generated_texts WHERE user_id = $1`,
		`DELETE FROM tatoeba_links WHERE en_id IN (SELECT tatoeba_id FROM cloze_items WHERE user_id = $1)`,
		`DELETE FROM tatoeba_sentences WHERE id IN (SELECT tatoeba_id FROM cloze_items WHERE user_id = $1)`,
	} {
		if _, err := f.conn.Exec(ctx, sql, f.aliceID); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for _, w := range []string{"river", "apple", "garden", "bank"} {
		if n := left(w); n != 0 {
			t.Errorf("у %s после удаления источника осталось %d строк", w, n)
		}
	}
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

// clozeObjects — таблицы и индексы 0022 (to_regclass по порядку).
const clozeObjects = "cloze_items,cloze_misses,cloze_distractors,cloze_attempts,utterances_text_tsv_idx,materials_body_tsv_idx"

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
		if err := f.conn.QueryRow(ctx, `SELECT concat_ws(',', to_regclass('cloze_items'), to_regclass('cloze_misses'),
			to_regclass('cloze_distractors'), to_regclass('cloze_attempts'), to_regclass('utterances_text_tsv_idx'),
			to_regclass('materials_body_tsv_idx'))`).Scan(&present); err != nil {
			t.Fatal(err)
		}
		return present
	}
	if got := tables(); got != clozeObjects {
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
	if got := tables(); got != clozeObjects {
		t.Fatalf("повторный накат: %q", got)
	}
	if resp, _ := f.get(t, f.alice); len(resp.Items) != 1 {
		t.Fatalf("выдача после наката: %+v", resp.Items)
	}
}
