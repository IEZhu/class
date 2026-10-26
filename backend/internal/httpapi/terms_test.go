package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/dictionary"
	"github.com/IEZhu/class/backend/internal/store"
)

// fakeLibreTranslate — POST /translate LibreTranslate: переводит по
// словарю, считает вызовы; down — сервис отвечает 503.
type fakeLibreTranslate struct {
	srv   *httptest.Server
	calls atomic.Int32
	down  atomic.Bool
	mu    sync.Mutex
	asked []string
}

func newFakeLibreTranslate(t *testing.T, words map[string]string) *fakeLibreTranslate {
	t.Helper()
	f := &fakeLibreTranslate{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		var req struct{ Q, Source, Target, Format string }
		if r.Method != http.MethodPost || r.URL.Path != "/translate" ||
			json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.asked = append(f.asked, req.Source+">"+req.Target+":"+req.Q)
		f.mu.Unlock()
		if f.down.Load() {
			http.Error(w, `{"error":"models are loading"}`, http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"translatedText": words[req.Q]})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLibreTranslate) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// termsAPI — api поверх изолированной схемы securityDB с импортированной
// фикстурой словаря и LibreTranslate по адресу translateURL (пусто — без
// перевода).
func termsAPI(t *testing.T, translateURL string) (*API, *pgx.Conn, *http.Cookie) {
	t.Helper()
	base, conn := securityDB(t)
	importFixture(t, base, dictionaryFixture, dictionary.Options{Source: "fixture"})
	_, cookie := securityUser(t, base, conn, store.RoleStudent, "clicker")
	a, err := New(base.store, Config{LibreTranslateURL: translateURL})
	if err != nil {
		t.Fatal(err)
	}
	return a, conn, cookie
}

func click(t *testing.T, h http.Handler, cookie *http.Cookie, body string) termClickResponse {
	t.Helper()
	w := securityRequest(h, "POST", "/terms/click", body, cookie)
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"examples":[]`) {
		t.Fatalf("examples (долг D-1) — пустой список, не null: %s", w.Body.String())
	}
	var resp termClickResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// DoD S3-2: первый клик переводит лемму через LibreTranslate и пишет кэш,
// повторный — в любой форме слова — во внешние сервисы не ходит.
func TestTermClickTranslatesOnceThenCaches(t *testing.T) {
	lt := newFakeLibreTranslate(t, map[string]string{"run": "бежать"})
	a, conn, student := termsAPI(t, lt.srv.URL)
	h := a.Router()

	first := click(t, h, student, `{"surface":"Running","context_sentence":"He is running home."}`)
	if first.Lemma != "run" || first.Surface != "Running" || first.TermID == 0 {
		t.Fatalf("клик: %+v", first)
	}
	if first.Translation == nil || *first.Translation != (termTranslation{Text: "бежать", Source: "libretranslate"}) {
		t.Fatalf("перевод: %+v", first.Translation)
	}
	if got := lt.requests(); len(got) != 1 || got[0] != "en>ru:run" {
		t.Fatalf("LibreTranslate: %v, ждали один перевод леммы", got)
	}
	// Дефиниции из датасета: сначала глагол (первая статья страницы),
	// у дубля секции побеждает первая
	if len(first.Definitions) != 2 || first.Definitions[0].Pos != "verb" ||
		first.Definitions[0].Glosses[0] != "To move swiftly on foot." || first.Definitions[1].Pos != "noun" ||
		strings.Join(first.Definitions[0].TranslationsRU, ",") != "бегать,бежать" {
		t.Fatalf("дефиниции: %+v", first.Definitions)
	}
	var cached string
	if err := conn.QueryRow(context.Background(),
		`SELECT translation_cache->'ru'->>'text' FROM terms WHERE lang = 'en' AND lemma = 'run'`).Scan(&cached); err != nil || cached != "бежать" {
		t.Fatalf("кэш перевода: %q, %v", cached, err)
	}

	for _, body := range []string{
		`{"surface":"Running","context_sentence":"He is running home."}`,
		`{"surface":"ran"}`,
		`{"surface":"runs","context_sentence":"She runs a school."}`,
	} {
		again := click(t, h, student, body)
		if again.TermID != first.TermID || again.Translation == nil || again.Translation.Text != "бежать" {
			t.Fatalf("повтор %s: %+v", body, again)
		}
	}
	if lt.calls.Load() != 1 {
		t.Fatalf("повторные клики сходили в LibreTranslate: %d вызовов", lt.calls.Load())
	}
	var terms int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM terms`).Scan(&terms); err != nil || terms != 1 {
		t.Fatalf("терминов %d, ждали один на лемму: %v", terms, err)
	}
}

// LibreTranslate лежит или не настроен — попап всё равно работает:
// 200, дефиниции, translation = null; неудача не кэшируется, и поднявшийся
// сервис переведёт слово на следующем клике.
func TestTermClickWithoutLibreTranslate(t *testing.T) {
	lt := newFakeLibreTranslate(t, map[string]string{"bank": "банк"})
	lt.down.Store(true)
	a, conn, student := termsAPI(t, lt.srv.URL)
	h := a.Router()

	resp := click(t, h, student, `{"surface":"banks"}`)
	if resp.Translation != nil || resp.Lemma != "bank" || len(resp.Definitions) != 2 ||
		resp.Definitions[1].Glosses[0] != "The edge of a river." || resp.Definitions[1].TranslationsRU[0] != "берег" {
		t.Fatalf("без перевода: %+v", resp)
	}
	var cache string
	if err := conn.QueryRow(context.Background(),
		`SELECT translation_cache::text FROM terms WHERE lemma = 'bank'`).Scan(&cache); err != nil || cache != "{}" {
		t.Fatalf("неудачный перевод попал в кэш: %s, %v", cache, err)
	}

	lt.down.Store(false)
	if resp := click(t, h, student, `{"surface":"bank"}`); resp.Translation == nil || resp.Translation.Text != "банк" {
		t.Fatalf("сервис поднялся, а перевода нет: %+v", resp)
	}
	if lt.calls.Load() != 2 {
		t.Fatalf("вызовов LibreTranslate %d, ждали 2", lt.calls.Load())
	}

	// Сервис недоступен совсем (контейнер остановлен)
	lt.srv.Close()
	if resp := click(t, h, student, `{"surface":"building"}`); resp.Translation != nil || len(resp.Definitions) != 1 {
		t.Fatalf("недоступный сервис: %+v", resp)
	}

	// Ключа нет вовсе: стенд без LibreTranslate
	plain, err := New(a.store, Config{})
	if err != nil {
		t.Fatal(err)
	}
	resp = click(t, plain.Router(), student, `{"surface":"the"}`)
	if resp.Translation != nil || len(resp.Definitions) != 1 || resp.Definitions[0].Pos != "article" {
		t.Fatalf("без LIBRETRANSLATE_URL: %+v", resp)
	}
	// Слова нет ни в датасете, ни в переводчике — пустые списки, не null
	w := securityRequest(plain.Router(), "POST", "/terms/click", `{"surface":"zzyzxq"}`, student)
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"definitions":[]`) || !strings.Contains(w.Body.String(), `"translation":null`) {
		t.Fatalf("незнакомое слово: %s", w.Body.String())
	}
}

// Незнакомое слово (нет ни в OEWN, ни в датасете) отвечается без
// термина: общий словарь не засоряется, LibreTranslate не занимается.
// Знакомое — термин и перевод, как всегда; служебное слово знакомо
// по статье датасета.
func TestTermClickUnknownWordIsNotStored(t *testing.T) {
	lt := newFakeLibreTranslate(t, map[string]string{"bank": "банк", "the": "тот"})
	a, conn, student := termsAPI(t, lt.srv.URL)
	h := a.Router()
	terms := func() int {
		t.Helper()
		var n int
		if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM terms`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for _, surface := range []string{"qzxvbn", "Qzxvbn", "zzyzxq"} {
		resp := click(t, h, student, `{"surface":"`+surface+`"}`)
		if resp.TermID != 0 || resp.Translation != nil || len(resp.Definitions) != 0 ||
			resp.Lemma != strings.ToLower(surface) || resp.Surface != surface {
			t.Fatalf("незнакомое слово %s: %+v", surface, resp)
		}
	}
	if n := terms(); n != 0 || lt.calls.Load() != 0 {
		t.Fatalf("незнакомые слова: терминов %d, вызовов LibreTranslate %d; ждали 0 и 0", n, lt.calls.Load())
	}

	if resp := click(t, h, student, `{"surface":"bank"}`); resp.TermID == 0 || resp.Translation == nil ||
		resp.Translation.Text != "банк" {
		t.Fatalf("знакомое слово: %+v", resp)
	}
	if resp := click(t, h, student, `{"surface":"The"}`); resp.TermID == 0 || resp.Translation == nil ||
		len(resp.Definitions) != 1 {
		t.Fatalf("служебное слово из датасета: %+v", resp)
	}
	if again := click(t, h, student, `{"surface":"banks"}`); again.Translation == nil || again.Translation.Text != "банк" {
		t.Fatalf("повтор знакомого слова: %+v", again)
	}
	if n := terms(); n != 2 || lt.calls.Load() != 2 {
		t.Fatalf("терминов %d, вызовов LibreTranslate %d; ждали 2 и 2 (повтор — из кэша)", n, lt.calls.Load())
	}
}

// Выдача статей: сначала лемма термина, затем сама словоформа (building —
// это и build, и building), внутри — точный регистр клика первым.
func TestTermClickDefinitionsOrder(t *testing.T) {
	a, _, student := termsAPI(t, "")
	h := a.Router()
	if resp := click(t, h, student, `{"surface":"building"}`); resp.Lemma != "build" ||
		len(resp.Definitions) != 1 || resp.Definitions[0].Word != "building" {
		t.Fatalf("building: %+v", resp)
	}
	words := func(r termClickResponse) string {
		var ws []string
		for _, d := range r.Definitions {
			ws = append(ws, d.Word)
		}
		return strings.Join(ws, ",")
	}
	if got := words(click(t, h, student, `{"surface":"Polish"}`)); got != "Polish,polish" {
		t.Fatalf("Polish: %s", got)
	}
	if got := words(click(t, h, student, `{"surface":"polish"}`)); got != "polish,Polish" {
		t.Fatalf("polish: %s", got)
	}
}

func TestTermClickValidation(t *testing.T) {
	a, conn, student := termsAPI(t, "")
	h := a.Router()
	ctx := context.Background()
	expectStatus(t, securityRequest(h, "POST", "/terms/click", `{"surface":"run"}`, nil), http.StatusUnauthorized)
	for _, body := range []string{
		`{}`,
		`{"surface":""}`,
		`{"surface":"   "}`,
		`{"surface":"two words"}`,
		`{"surface":"42"}`,
		`{"surface":"-run"}`,
		`{"surface":"` + strings.Repeat("a", 65) + `"}`,
		`{"surface":"run","context_sentence":"` + strings.Repeat("я", 1001) + `"}`,
		`{"surface":"run","lesson_id":0}`,
		`{"surface":"run","lesson_id":-5}`,
		`{"surface":"run","lesson_id":"1"}`,
		`{"surface":1}`,
		`not json`,
		`{"surface":"run"}{"surface":"walk"}`,
	} {
		w := securityRequest(h, "POST", "/terms/click", body, student)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%.60s: status %d, хотели 400; %s", body, w.Code, w.Body.String())
		}
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM terms`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("невалидные клики завели %d терминов: %v", n, err)
	}

	// lesson_id: свой урок — 200, чужой — 403, несуществующий — 404
	teacherID, owner := securityUser(t, a, conn, store.RoleTeacher, "owner")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('words','B1') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	lesson, err := a.store.CreateLesson(ctx, &groupID, "", teacherID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"surface":"run","context_sentence":"Run!","lesson_id":%d}`, lesson.ID)
	expectStatus(t, securityRequest(h, "POST", "/terms/click", body, student), http.StatusForbidden)
	_, teacher := securityUser(t, a, conn, store.RoleTeacher, "teacher-clicker")
	expectStatus(t, securityRequest(h, "POST", "/terms/click", body, teacher), http.StatusForbidden)
	expectStatus(t, securityRequest(h, "POST", "/terms/click", body, owner), http.StatusOK)
	expectStatus(t, securityRequest(h, "POST", "/terms/click",
		fmt.Sprintf(`{"surface":"run","lesson_id":%d}`, lesson.ID+1000), student), http.StatusNotFound)
}
