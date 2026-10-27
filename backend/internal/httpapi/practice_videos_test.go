package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/store"
)

// S5-5 на общей тестовой базе: подбор видео корпуса субтитров под слова
// словаря (веса по статусу и по карточкам FSRS), страницы, доступ,
// субтитры одного видео и план запроса на засеянном корпусе. В YouTube
// тесты не ходят: видео и сегменты пишутся в базу напрямую.

type videoFixture struct {
	a       *API
	conn    *pgx.Conn
	student *http.Cookie
	userID  int64
	ids     map[string]int64
}

// addPracticeVideo — видео канала TED с сегментами по 5 с; kind "" —
// субтитры ещё не собраны.
func (f *videoFixture) addPracticeVideo(t *testing.T, externalID string, durationS int, kind string, texts ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.a.store.AddFeedVideos(ctx, subtitleSourceID(t, f.conn, tedChannelID),
		[]store.FeedVideo{{ExternalID: externalID, Title: "Talk " + externalID}}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := f.conn.QueryRow(ctx, `SELECT id FROM videos WHERE external_id = $1`, externalID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.ids[externalID] = id
	if kind == "" {
		return
	}
	segments := make([]store.SubtitleSegment, len(texts))
	for i, text := range texts {
		segments[i] = store.SubtitleSegment{StartMS: i * 5000, EndMS: i*5000 + 4000, Text: text}
	}
	if err := f.a.store.SaveSubtitles(ctx, id, kind, durationS, segments); err != nil {
		t.Fatal(err)
	}
}

// learnWords кладёт слова в словарь пользователя с заданными статусами.
func (f *videoFixture) learnWords(t *testing.T, userID int64, statuses map[string]int) {
	t.Helper()
	var lemmas []string
	var values []int
	for l, s := range statuses {
		lemmas, values = append(lemmas, l), append(values, s)
	}
	if _, err := f.conn.Exec(context.Background(),
		`WITH t AS (INSERT INTO terms (lemma, surface) SELECT l, l FROM unnest($2::text[]) l
		            ON CONFLICT (lang, lemma) DO UPDATE SET surface = terms.surface RETURNING id, lemma)
		 INSERT INTO user_terms (user_id, term_id, status)
		 SELECT $1, t.id, s.status FROM t JOIN unnest($2::text[], $3::int[]) AS s(lemma, status) USING (lemma)`,
		userID, lemmas, values); err != nil {
		t.Fatal(err)
	}
}

// newVideoFixture — корпус из семи видео и студент со словами cat (1),
// dog (2), go (3) и выученным mat (5).
func newVideoFixture(t *testing.T) *videoFixture {
	t.Helper()
	a, conn := securityDB(t)
	f := &videoFixture{a: a, conn: conn, ids: map[string]int64{}}
	f.userID, f.student = securityUser(t, a, conn, store.RoleStudent, "video-student")
	f.learnWords(t, f.userID, map[string]int{"cat": 1, "dog": 2, "go": 3, "mat": 5})
	// Все три слова, go — неправильной формой went
	f.addPracticeVideo(t, "allThree001", 600, store.SubtitlesManual,
		"Nothing to see here.", "A cat is here.", "The cat and the dog went home.")
	// Одно cat, но густо: три вхождения за минуту
	f.addPracticeVideo(t, "catLots0001", 60, store.SubtitlesAuto, "Cats!", "My cat sleeps.", "The cat again.")
	// То же покрытие, реже: одно вхождение за десять минут
	f.addPracticeVideo(t, "catSlow0001", 600, store.SubtitlesManual, "A cat.")
	f.addPracticeVideo(t, "dogOnly0001", 120, store.SubtitlesManual, "The dogs bark.")
	// Только выученное слово — в подбор по словарю не попадает
	f.addPracticeVideo(t, "matOnly0001", 60, store.SubtitlesManual, "A red mat.")
	// Субтитров нет: сборщик записал none (сегменты — остаток прошлого
	// сбора) или ещё не собирал
	f.addPracticeVideo(t, "noneStale01", 60, store.SubtitlesNone, "Cat, dog, went, mat.")
	f.addPracticeVideo(t, "pending0001", 0, "")
	return f
}

func (f *videoFixture) dropSRSCards(t *testing.T) {
	t.Helper()
	// Своя схема теста: после S4-1 таблица есть, здесь проверяется база без неё
	if _, err := f.conn.Exec(context.Background(), `DROP TABLE IF EXISTS srs_cards CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func getVideos(t *testing.T, h http.Handler, cookie *http.Cookie, query string) practiceVideosPage {
	t.Helper()
	w := securityRequest(h, "GET", "/practice/videos"+query, "", cookie)
	expectStatus(t, w, http.StatusOK)
	var page practiceVideosPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func videoOrder(page practiceVideosPage) string {
	var ids []string
	for _, v := range page.Items {
		ids = append(ids, v.YouTubeID)
	}
	return strings.Join(ids, " ")
}

func targetWords(page videoTargetsResponse) string {
	var words []string
	for _, w := range page.Words {
		words = append(words, fmt.Sprintf("%s:%g", w.Lemma, w.Weight))
	}
	return page.Weighting + " " + strings.Join(words, " ")
}

// Без srs_cards веса — по статусу: видео со всеми тремя словами впереди,
// равное покрытие решает плотность вхождений; у видео — лучшие места,
// сначала сегмент с большим весом слов. Выученные слова и видео без
// субтитров в подбор не попадают.
func TestPracticeVideosRankByLearningWords(t *testing.T) {
	f := newVideoFixture(t)
	f.dropSRSCards(t)
	h := f.a.Router()

	page := getVideos(t, h, f.student, "")
	if got := targetWords(page.videoTargetsResponse); got != "status cat:4 dog:3 go:2" {
		t.Fatalf("цели %s", got)
	}
	if got := videoOrder(page); got != "allThree001 catLots0001 catSlow0001 dogOnly0001" {
		t.Fatalf("порядок %s", got)
	}
	if page.NextCursor != nil {
		t.Fatalf("курсор на единственной странице: %s", *page.NextCursor)
	}
	best := page.Items[0]
	if !reflect.DeepEqual(best.Words, []string{"cat", "dog", "go"}) || best.Coverage != 1 ||
		best.Occurrences != 4 || best.DurationMS != 600000 || best.Subtitles != store.SubtitlesManual ||
		best.URL != "https://www.youtube.com/watch?v=allThree001" {
		t.Fatalf("лучшее видео %+v", best)
	}
	var places []string
	for _, s := range best.Segments {
		places = append(places, fmt.Sprintf("%d@%d-%d:%s", s.Idx, s.StartMS, s.EndMS, strings.Join(s.Words, ",")))
	}
	if got := strings.Join(places, " "); got != "2@10000-14000:cat,dog,go 1@5000-9000:cat" {
		t.Fatalf("лучшие места %s", got)
	}
	if lots, slow := page.Items[1], page.Items[2]; lots.PerMinute != 3 || slow.PerMinute != 0.1 ||
		lots.Coverage != slow.Coverage {
		t.Fatalf("плотность %+v / %+v", lots, slow)
	}

	// Слова из запроса: вес 1, формы сводятся к лемме, выученное mat
	// ищется; при равном покрытии — гуще на минуту
	page = getVideos(t, h, f.student, "?words=Dogs,mat&words=dogs")
	if got := targetWords(page.videoTargetsResponse); got != "words dog:1 mat:1" {
		t.Fatalf("цели из запроса %s", got)
	}
	if got := videoOrder(page); got != "matOnly0001 dogOnly0001 allThree001" {
		t.Fatalf("порядок по словам запроса %s", got)
	}
}

// С srs_cards (0021, S4-1) веса — по карточкам FSRS: dog к повторению
// и забыт десять раз, но забывания в весе — не больше трёх (2 + 3), у go
// карточки нет (как к повторению), cat повторять не пора. dogOnly обгоняет
// cat-видео, хотя по статусу cat новее.
func TestPracticeVideosSRSWeights(t *testing.T) {
	f := newVideoFixture(t)
	ctx := context.Background()
	// Фикстура пишет слова в обход хука SaveUserTerm: карточки заводит
	// бэкфилл, как worker при старте, — в настоящей таблице 0021
	if n, err := f.a.store.BackfillSRSCards(ctx); err != nil || n != 4 {
		t.Fatalf("бэкфилл карточек: %d, %v", n, err)
	}
	card := func(word string, due time.Time, lapses int) {
		t.Helper()
		tag, err := f.conn.Exec(ctx, `UPDATE srs_cards SET due_at = $3, lapses = $4
			WHERE user_id = $1 AND term_id = (SELECT id FROM terms WHERE lemma = $2)`,
			f.userID, word, due, lapses)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("карточка %s: %v, строк %d", word, err, tag.RowsAffected())
		}
	}
	card("cat", time.Now().Add(24*time.Hour), 5)
	card("dog", time.Now().Add(-time.Hour), 10)
	if _, err := f.conn.Exec(ctx, `DELETE FROM srs_cards WHERE term_id = (SELECT id FROM terms WHERE lemma = 'go')`); err != nil {
		t.Fatal(err)
	}

	page := getVideos(t, f.a.Router(), f.student, "")
	if got := targetWords(page.videoTargetsResponse); got != "srs dog:5 go:2 cat:1" {
		t.Fatalf("цели %s", got)
	}
	if got := videoOrder(page); got != "allThree001 dogOnly0001 catLots0001 catSlow0001" {
		t.Fatalf("порядок %s", got)
	}
}

// fakeLearningWords — источник весов вместо словаря (шов learningWordSource).
type fakeLearningWords []store.LearningWord

func (f fakeLearningWords) LearningWords(context.Context, int64, string, int) ([]store.LearningWord, string, error) {
	return f, "fake", nil
}

// Ранжирование берёт веса из источника как есть; слово с нулевым весом
// и не-слово в поиск не идут.
func TestPracticeVideosWordSource(t *testing.T) {
	f := newVideoFixture(t)
	f.a.learningWords = fakeLearningWords{{Lemma: "mat", Weight: 5}, {Lemma: "dog", Weight: 1},
		{Lemma: "cat", Weight: 0}, {Lemma: "w1", Weight: 3}}
	page := getVideos(t, f.a.Router(), f.student, "")
	if got := targetWords(page.videoTargetsResponse); got != "fake mat:5 dog:1" {
		t.Fatalf("цели %s", got)
	}
	if got := videoOrder(page); got != "matOnly0001 dogOnly0001 allThree001" {
		t.Fatalf("порядок %s", got)
	}
}

// Страницы по курсору-позиции и разбор параметров.
func TestPracticeVideosPagination(t *testing.T) {
	f := newVideoFixture(t)
	f.dropSRSCards(t)
	h := f.a.Router()
	first := getVideos(t, h, f.student, "?limit=3")
	if got := videoOrder(first); got != "allThree001 catLots0001 catSlow0001" || first.NextCursor == nil {
		t.Fatalf("первая страница %s, курсор %v", got, first.NextCursor)
	}
	second := getVideos(t, h, f.student, "?limit=3&cursor="+*first.NextCursor)
	if got := videoOrder(second); got != "dogOnly0001" || second.NextCursor != nil {
		t.Fatalf("вторая страница %s, курсор %v", got, second.NextCursor)
	}
	if past := getVideos(t, h, f.student, "?cursor=40"); len(past.Items) != 0 || past.NextCursor != nil {
		t.Fatalf("за концом выдачи %+v", past)
	}
	many := make([]string, 51)
	for i := range many {
		many[i] = fmt.Sprintf("word%c%c", 'a'+i/26, 'a'+i%26)
	}
	for _, q := range []string{"?limit=0", "?limit=51", "?limit=x", "?cursor=-1", "?cursor=1001", "?cursor=a",
		"?words=1abc", "?words=ice%20cream", "?words=" + strings.Join(many, ",")} {
		expectStatus(t, securityRequest(h, "GET", "/practice/videos"+q, "", f.student), http.StatusBadRequest)
	}
}

// Видео-практика — у студента и преподавателя (как тексты, S5-6): без
// входа 401, админ и гость события — 403. У преподавателя словаря нет:
// подбор по своим словам пуст, по словам запроса работает.
func TestPracticeVideosAccess(t *testing.T) {
	f := newVideoFixture(t)
	h := f.a.Router()
	paths := []string{"/practice/videos", fmt.Sprintf("/practice/videos/%d", f.ids["allThree001"])}
	for _, p := range paths {
		expectStatus(t, securityRequest(h, "GET", p, "", nil), http.StatusUnauthorized)
		for _, role := range []string{store.RoleAdmin, store.RoleGuest} {
			_, cookie := securityUser(t, f.a, f.conn, role, "video-"+role+fmt.Sprint(len(p)))
			expectStatus(t, securityRequest(h, "GET", p, "", cookie), http.StatusForbidden)
		}
	}
	_, teacher := securityUser(t, f.a, f.conn, store.RoleTeacher, "video-teacher")
	if page := getVideos(t, h, teacher, ""); len(page.Words) != 0 || len(page.Items) != 0 {
		t.Fatalf("подбор преподавателя без слов %+v", page)
	}
	if got := videoOrder(getVideos(t, h, teacher, "?words=dog")); got != "dogOnly0001 allThree001" {
		t.Fatalf("подбор преподавателя по dog: %s", got)
	}
	// Чужой словарь в подбор не попадает
	otherID, other := securityUser(t, f.a, f.conn, store.RoleStudent, "video-other")
	f.learnWords(t, otherID, map[string]int{"mat": 1})
	if got := videoOrder(getVideos(t, h, other, "")); got != "matOnly0001" {
		t.Fatalf("подбор другого студента %s", got)
	}
}

// Субтитры одного видео для плеера: все сегменты по порядку, найденные
// цели — у своих сегментов; видео без субтитров и чужие пути — 404/400.
func TestPracticeVideoSubtitles(t *testing.T) {
	f := newVideoFixture(t)
	f.dropSRSCards(t)
	h := f.a.Router()
	w := securityRequest(h, "GET", fmt.Sprintf("/practice/videos/%d", f.ids["allThree001"]), "", f.student)
	expectStatus(t, w, http.StatusOK)
	var detail practiceVideoDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if got := targetWords(detail.videoTargetsResponse); got != "status cat:4 dog:3 go:2" {
		t.Fatalf("цели %s", got)
	}
	v := detail.Video
	var lines []string
	for _, s := range v.Segments {
		lines = append(lines, fmt.Sprintf("%d@%d:%s[%s]", s.Idx, s.StartMS, s.Text, strings.Join(s.Words, ",")))
	}
	want := "0@0:Nothing to see here.[] 1@5000:A cat is here.[cat] 2@10000:The cat and the dog went home.[cat,dog,go]"
	if got := strings.Join(lines, " "); got != want {
		t.Fatalf("сегменты %s", got)
	}
	// Счётчики — те же, что у видео в подборе
	if v.Coverage != 1 || v.Occurrences != 4 || v.PerMinute != 0.4 || v.YouTubeID != "allThree001" ||
		!reflect.DeepEqual(v.Words, []string{"cat", "dog", "go"}) {
		t.Fatalf("видео %+v", v)
	}
	// Без длительности в videos — по концу последнего сегмента
	if _, err := f.conn.Exec(context.Background(), `UPDATE videos SET duration_s = NULL WHERE id = $1`, f.ids["catLots0001"]); err != nil {
		t.Fatal(err)
	}
	w = securityRequest(h, "GET", fmt.Sprintf("/practice/videos/%d?words=sleep", f.ids["catLots0001"]), "", f.student)
	expectStatus(t, w, http.StatusOK)
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Video.DurationMS != 14000 || detail.Weighting != weightsWords ||
		!reflect.DeepEqual(detail.Video.Segments[1].Words, []string{"sleep"}) {
		t.Fatalf("видео без длительности %+v", detail)
	}
	for path, status := range map[string]int{
		fmt.Sprintf("/practice/videos/%d", f.ids["noneStale01"]): http.StatusNotFound,
		fmt.Sprintf("/practice/videos/%d", f.ids["pending0001"]): http.StatusNotFound,
		"/practice/videos/999999":                                http.StatusNotFound,
		"/practice/videos/abc":                                   http.StatusBadRequest,
	} {
		expectStatus(t, securityRequest(h, "GET", path, "", f.student), status)
	}
}

// План подбора на засеянном корпусе: каждая цель ищется GIN-индексом tsv,
// а не перебором сегментов. Корпус — 300 видео по 100 сегментов, слова
// с частотами по закону Ципфа (как в живом тексте); 50 целей средней
// частоты — потолок maxVideoWords.
func TestPracticeVideosPlanUsesIndex(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	const videos, segments, vocabulary = 300, 100, 20000
	if _, err := conn.Exec(ctx, `INSERT INTO videos (source, external_id, title, duration_s, subtitles)
		SELECT 'youtube', 'synthetic' || g, 'Video ' || g, 600, 'manual' FROM generate_series(1, $1) g`, videos); err != nil {
		t.Fatal(err)
	}
	// Подзапрос ссылается на s, иначе Postgres посчитал бы текст один раз
	if _, err := conn.Exec(ctx, `INSERT INTO subtitle_segments (video_id, idx, start_ms, end_ms, text)
		SELECT v.id, s, s * 4000, s * 4000 + 3500,
		       (SELECT string_agg('w' || floor(exp(random() * ln($2::float8)))::int, ' ')
		        FROM generate_series(1, 10) WHERE s >= 0)
		FROM videos v, generate_series(0, $1 - 1) s`, segments, vocabulary); err != nil {
		t.Fatal(err)
	}
	// Как после autovacuum: статистика собрана, список ожидания GIN
	// (fastupdate) влит в индекс — иначе каждый проход читает его целиком
	if _, err := conn.Exec(ctx, `VACUUM ANALYZE videos, subtitle_segments`); err != nil {
		t.Fatal(err)
	}
	targets := make([]store.VideoTarget, maxVideoWords)
	for i := range targets {
		w := fmt.Sprintf("w%d", 100+97*i)
		targets[i] = store.VideoTarget{Lemma: w, Forms: []string{w}, Weight: float64(1 + i%4)}
	}
	query, args := store.PracticeVideosArgs(targets, defaultVideosLimit+1, 0)
	rows, err := conn.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF) "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(plan, "\n")
	t.Logf("%d сегментов, %d целей:\n%s", videos*segments, len(targets), text)
	if !strings.Contains(text, "Bitmap Index Scan on subtitle_segments_tsv_idx") || strings.Contains(text, "Seq Scan on subtitle_segments") {
		t.Fatalf("подбор не идёт по GIN-индексу tsv:\n%s", text)
	}
	started := time.Now()
	page, err := a.store.PracticeVideos(ctx, targets, defaultVideosLimit+1, 0, videoBestSegments)
	if err != nil || len(page) == 0 {
		t.Fatalf("подбор на корпусе: %d видео, %v", len(page), err)
	}
	t.Logf("PracticeVideos: %v", time.Since(started))
}
