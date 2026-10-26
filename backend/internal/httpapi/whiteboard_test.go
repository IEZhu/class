package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/livekit/protocol/livekit"

	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/worker"
)

// Доска урока (S3-6, ADR-020) на той же изолированной схеме, что и
// security_test: настоящие миграции, настоящий роутер.

type whiteboardFixture struct {
	a        *API
	conn     *pgx.Conn
	h        http.Handler
	lessonID int64
	// teacher и student — участники урока; outsider — студент без доступа;
	// guest — гость другого события; eventID — его событие
	teacher, student, outsider, guest *http.Cookie
	eventID                           int64
}

func newWhiteboardFixture(t *testing.T) *whiteboardFixture {
	t.Helper()
	a, conn := securityDB(t)
	a.cfg.WhiteboardEnabled = true
	ctx := context.Background()
	teacherID, teacher := securityUser(t, a, conn, store.RoleTeacher, "board-teacher")
	studentID, student := securityUser(t, a, conn, store.RoleStudent, "board-student")
	_, outsider := securityUser(t, a, conn, store.RoleStudent, "board-outsider")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('board','B1') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, groupID, studentID); err != nil {
		t.Fatal(err)
	}
	lesson, err := a.store.CreateLesson(ctx, &groupID, "", teacherID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	// Гость приходит по-настоящему — по ссылке на чужое событие
	event := openEvent(t, a, teacherID, "Разговорный клуб")
	w := securityRequest(h, "POST", "/join/"+issueGuestLink(t, h, event.ID, teacher), `{"name":"Гость"}`, nil)
	expectStatus(t, w, http.StatusCreated)
	return &whiteboardFixture{a: a, conn: conn, h: h, lessonID: lesson.ID,
		teacher: teacher, student: student, outsider: outsider, guest: w.Result().Cookies()[0], eventID: event.ID}
}

func (f *whiteboardFixture) path(suffix string) string {
	return fmt.Sprintf("/lessons/%d/whiteboard%s", f.lessonID, suffix)
}

// auth — запрос caddy forward_auth к api перед запросом к ретранслятору.
func (f *whiteboardFixture) auth(uri string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/whiteboard/auth", nil)
	r.Header.Set("X-Forwarded-Method", http.MethodGet)
	r.Header.Set("X-Forwarded-Uri", uri)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// randomRoom — имя комнаты и ключ: 128 бит в base64url без паддинга
var randomRoom = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// open — вход на доску урока участником.
func (f *whiteboardFixture) open(t *testing.T, cookie *http.Cookie) whiteboardResponse {
	t.Helper()
	w := securityRequest(f.h, "GET", f.path(""), "", cookie)
	expectStatus(t, w, http.StatusOK)
	var resp whiteboardResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func (f *whiteboardFixture) socketURI(lessonID int64) string {
	return fmt.Sprintf("/whiteboard/lesson-%d/socket.io/?EIO=4&transport=polling&t=abc", lessonID)
}

func (f *whiteboardFixture) setStatus(t *testing.T, status string) {
	t.Helper()
	if _, err := f.conn.Exec(context.Background(), `UPDATE lessons SET status=$2 WHERE id=$1`, f.lessonID, status); err != nil {
		t.Fatal(err)
	}
}

func element(id string, version int, text string) string {
	return fmt.Sprintf(`{"id":%q,"type":"text","version":%d,"versionNonce":1,"isDeleted":false,"text":%q}`, id, version, text)
}

func sceneBody(elements ...string) string {
	return `{"elements":[` + strings.Join(elements, ",") + `]}`
}

// Доску урока открывают только его участники — преподаватель и студенты
// группы; студент без доступа, гость чужого события и аноним — нет. Та же
// граница у forward_auth ретранслятора, у чтения и у сохранения сцены.
func TestWhiteboardAccessBoundary(t *testing.T) {
	f := newWhiteboardFixture(t)
	h := f.h

	var sessions []whiteboardResponse
	for _, cookie := range []*http.Cookie{f.teacher, f.student} {
		resp := f.open(t, cookie)
		if !randomRoom.MatchString(resp.Room) || !randomRoom.MatchString(resp.Key) || resp.Room == resp.Key ||
			resp.SocketPath != fmt.Sprintf("/whiteboard/lesson-%d/socket.io", f.lessonID) {
			t.Fatalf("whiteboard session %+v", resp)
		}
		sessions = append(sessions, resp)
		expectStatus(t, f.auth(f.socketURI(f.lessonID), cookie), http.StatusNoContent)
		expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(), cookie), http.StatusOK)
		expectStatus(t, securityRequest(h, "GET", f.path("/scene"), "", cookie), http.StatusOK)
	}
	// Одна доска — одна комната и один ключ на всех участников
	if sessions[0] != sessions[1] {
		t.Fatalf("participants got different rooms: %+v", sessions)
	}

	for _, cookie := range []*http.Cookie{f.outsider, f.guest} {
		w := securityRequest(h, "GET", f.path(""), "", cookie)
		expectStatus(t, w, http.StatusForbidden)
		if strings.Contains(w.Body.String(), sessions[0].Room) || strings.Contains(w.Body.String(), sessions[0].Key) {
			t.Fatalf("non-participant saw the room: %s", w.Body)
		}
		expectStatus(t, f.auth(f.socketURI(f.lessonID), cookie), http.StatusForbidden)
		expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(element("x", 1, "spam")), cookie), http.StatusForbidden)
		expectStatus(t, securityRequest(h, "GET", f.path("/scene"), "", cookie), http.StatusForbidden)
	}
	// Свою доску гость открывает
	expectStatus(t, f.auth(f.socketURI(f.eventID), f.guest), http.StatusNoContent)

	expectStatus(t, securityRequest(h, "GET", f.path(""), "", nil), http.StatusUnauthorized)
	expectStatus(t, f.auth(f.socketURI(f.lessonID), nil), http.StatusUnauthorized)
	expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(), nil), http.StatusUnauthorized)
	expectStatus(t, securityRequest(h, "GET", f.path("/scene"), "", nil), http.StatusUnauthorized)

	// Решение относится ровно к одному уроку: иные пути не пропускаются
	for _, uri := range []string{
		fmt.Sprintf("/whiteboard/lesson-%d/socket.io/../../lesson-%d/socket.io/", f.eventID, f.lessonID),
		fmt.Sprintf("/whiteboard/lesson-%d/socket.io/extra", f.lessonID),
		fmt.Sprintf("/whiteboard/lesson-%d/socket.io", f.lessonID),
		fmt.Sprintf("/whiteboard/lesson-%d/", f.lessonID),
		fmt.Sprintf("/api/lessons/%d", f.lessonID),
		"/whiteboard/lesson-0/socket.io/", "/whiteboard/room/socket.io/", "", "not a uri",
	} {
		if w := f.auth(uri, f.teacher); w.Code != http.StatusForbidden {
			t.Errorf("%q: status %d, want 403", uri, w.Code)
		}
	}
	expectStatus(t, f.auth("/whiteboard/lesson-999999/socket.io/", f.teacher), http.StatusNotFound)

	// После урока рисовать нельзя, а прочитать сцену — можно
	f.setStatus(t, "done")
	expectStatus(t, securityRequest(h, "GET", f.path(""), "", f.student), http.StatusConflict)
	expectStatus(t, f.auth(f.socketURI(f.lessonID), f.student), http.StatusConflict)
	expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(), f.student), http.StatusConflict)
	expectStatus(t, securityRequest(h, "GET", f.path("/scene"), "", f.student), http.StatusOK)

	// Без ретранслятора на стенде доска отвечает 503, сцена читается
	f.setStatus(t, "live")
	f.a.cfg.WhiteboardEnabled = false
	h = f.a.Router()
	f.h = h
	expectStatus(t, securityRequest(h, "GET", f.path(""), "", f.teacher), http.StatusServiceUnavailable)
	expectStatus(t, f.auth(f.socketURI(f.lessonID), f.teacher), http.StatusServiceUnavailable)
	expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(), f.teacher), http.StatusServiceUnavailable)
	expectStatus(t, securityRequest(h, "GET", f.path("/scene"), "", f.teacher), http.StatusOK)
}

// Ретранслятор пускает в любую комнату по имени, поэтому имя комнаты
// не выводится из id урока: у доски другого урока своё имя, а заведённая
// заново доска того же урока получает новое. В БД лежит выданное имя.
func TestWhiteboardRoomUnguessable(t *testing.T) {
	f := newWhiteboardFixture(t)
	ctx := context.Background()
	first := f.open(t, f.teacher)
	var stored string
	if err := f.conn.QueryRow(ctx, `SELECT room_id FROM whiteboards WHERE lesson_id = $1`, f.lessonID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != first.Room || strings.Contains(first.Room, "lesson") {
		t.Fatalf("room %q, stored %q", first.Room, stored)
	}

	other := securityRequest(f.h, "GET", fmt.Sprintf("/lessons/%d/whiteboard", f.eventID), "", f.teacher)
	expectStatus(t, other, http.StatusOK)
	var otherBoard whiteboardResponse
	if err := json.Unmarshal(other.Body.Bytes(), &otherBoard); err != nil {
		t.Fatal(err)
	}
	if otherBoard.Room == first.Room || otherBoard.Key == first.Key {
		t.Fatalf("two lessons share a room: %+v, %+v", first, otherBoard)
	}

	if _, err := f.conn.Exec(ctx, `DELETE FROM whiteboards WHERE lesson_id = $1`, f.lessonID); err != nil {
		t.Fatal(err)
	}
	again := f.open(t, f.student)
	if again.Room == first.Room || again.Key == first.Key || !randomRoom.MatchString(again.Room) {
		t.Fatalf("reopened board kept the room: %+v, was %+v", again, first)
	}
}

// Предел сцены — на итог слияния, а не на одно сохранение: сохранения
// по частям (каждое меньше 1 MiB) сцену дальше предела не растят — 413,
// сохранённое не меняется. Обычные сохранения и замена элемента новой
// версией того же размера проходят.
func TestWhiteboardSceneTotalLimit(t *testing.T) {
	f := newWhiteboardFixture(t)
	h := f.h
	f.open(t, f.teacher)
	big := func(id string, version int) string {
		return element(id, version, strings.Repeat("x", maxSceneBytes*6/10))
	}
	saved := func() string {
		t.Helper()
		w := securityRequest(h, "GET", f.path("/scene"), "", f.teacher)
		expectStatus(t, w, http.StatusOK)
		var file struct {
			Elements []struct {
				ID      string `json:"id"`
				Version int    `json:"version"`
			} `json:"elements"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &file); err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, e := range file.Elements {
			out = append(out, fmt.Sprintf("%s@%d", e.ID, e.Version))
		}
		return strings.Join(out, ",")
	}

	expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(big("a", 1)), f.teacher), http.StatusOK)
	w := securityRequest(h, "PUT", f.path("/scene"), sceneBody(big("b", 1)), f.student)
	expectStatus(t, w, http.StatusRequestEntityTooLarge)
	if !strings.Contains(w.Body.String(), `"scene_too_large"`) {
		t.Fatalf("413 body %s", w.Body)
	}
	if got := saved(); got != "a@1" {
		t.Fatalf("rejected save changed the scene: %s", got)
	}
	if b := f.board(t); b.sceneVersion != 1 {
		t.Fatalf("rejected save bumped the version to %d", b.sceneVersion)
	}

	expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(element("c", 1, "cat")), f.student), http.StatusOK)
	expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(big("a", 2)), f.teacher), http.StatusOK)
	if got := saved(); got != "a@2,c@1" {
		t.Fatalf("scene after normal saves: %s", got)
	}
}

type fakeScenes struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
	err     error
}

func (f *fakeScenes) PutObject(_ context.Context, key, contentType string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if contentType != "application/json" {
		return fmt.Errorf("content type %q", contentType)
	}
	f.puts++
	f.objects[key] = append([]byte(nil), body...)
	return nil
}

type snapshotJob struct {
	count    int
	attempts int
	dueIn    time.Duration
}

func (f *whiteboardFixture) snapshotJobs(t *testing.T) snapshotJob {
	t.Helper()
	var j snapshotJob
	var seconds *float64
	err := f.conn.QueryRow(context.Background(),
		`SELECT count(*), coalesce(max(attempts), 0), max(extract(epoch FROM run_at - now()))::float8
		 FROM jobs WHERE kind = $1 AND payload = $2`,
		store.JobWhiteboardSnapshot, map[string]int64{"lesson_id": f.lessonID}).Scan(&j.count, &j.attempts, &seconds)
	if err != nil {
		t.Fatal(err)
	}
	if seconds != nil {
		j.dueIn = time.Duration(*seconds * float64(time.Second))
	}
	return j
}

type boardRow struct {
	sceneVersion, snapshotVersion int64
	key                           string
}

func (f *whiteboardFixture) board(t *testing.T) boardRow {
	t.Helper()
	var b boardRow
	var key *string
	if err := f.conn.QueryRow(context.Background(),
		`SELECT scene_version, snapshot_version, scene_s3_key FROM whiteboards WHERE lesson_id = $1`,
		f.lessonID).Scan(&b.sceneVersion, &b.snapshotVersion, &key); err != nil {
		t.Fatal(err)
	}
	if key != nil {
		b.key = *key
	}
	return b
}

func sceneTexts(t *testing.T, body []byte) string {
	t.Helper()
	var file struct {
		Type     string `json:"type"`
		Elements []struct {
			ID        string `json:"id"`
			Version   int    `json:"version"`
			Text      string `json:"text"`
			IsDeleted bool   `json:"isDeleted"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(body, &file); err != nil || file.Type != "excalidraw" {
		t.Fatalf("not an .excalidraw file (%v): %s", err, body)
	}
	out := []string{}
	for _, e := range file.Elements {
		state := ""
		if e.IsDeleted {
			state = "-"
		}
		out = append(out, fmt.Sprintf("%s%s@%d:%s", state, e.ID, e.Version, e.Text))
	}
	return strings.Join(out, ",")
}

// Сохранения разных участников сливаются поэлементно; пока рисуют, снимок
// ставится одной джобой не чаще интервала, конец урока (room_finished)
// снимает сразу. Снимок — файл .excalidraw в whiteboards/{id}.json;
// повтор джобы ничего не меняет, сбой хранилища — ретрай.
func TestWhiteboardSceneSavesAndSnapshots(t *testing.T) {
	f := newWhiteboardFixture(t)
	h := f.h
	ctx := context.Background()
	// Без открытия доски сохранять некуда
	expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(element("a", 1, "apple")), f.teacher), http.StatusNotFound)
	expectStatus(t, securityRequest(h, "GET", f.path("/scene"), "", f.teacher), http.StatusNotFound)
	expectStatus(t, securityRequest(h, "GET", f.path(""), "", f.teacher), http.StatusOK)
	if j := f.snapshotJobs(t); j.count != 0 {
		t.Fatalf("opening the board queued %d snapshots", j.count)
	}

	expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(element("a", 1, "apple")), f.teacher), http.StatusOK)
	// Студент ещё не получил «a» и прислал только своё — «a» не пропадает
	expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(element("b", 1, "pear")), f.student), http.StatusOK)
	// Отставшая версия «a» не перебивает новую, удаление «b» побеждает
	w := securityRequest(h, "PUT", f.path("/scene"), sceneBody(element("a", 2, "apples"),
		`{"id":"b","type":"text","version":2,"versionNonce":1,"isDeleted":true,"text":"pear"}`), f.teacher)
	expectStatus(t, w, http.StatusOK)
	expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), sceneBody(element("a", 1, "apple"), element("b", 1, "pear")), f.student), http.StatusOK)
	w = securityRequest(h, "GET", f.path("/scene"), "", f.student)
	expectStatus(t, w, http.StatusOK)
	if got := sceneTexts(t, w.Body.Bytes()); got != "a@2:apples,-b@2:pear" {
		t.Fatalf("merged scene %s", got)
	}
	if b := f.board(t); b.sceneVersion != 3 {
		t.Fatalf("scene version %d, want 3 (the stale save changed nothing)", b.sceneVersion)
	}

	for _, body := range []string{`{`, `[]`, `{"elements":[{"id":"a"}]}`, `{"elements":[1]}`} {
		expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), body, f.teacher), http.StatusBadRequest)
	}
	huge := sceneBody(element("big", 1, strings.Repeat("x", maxSceneBytes)))
	expectStatus(t, securityRequest(h, "PUT", f.path("/scene"), huge, f.teacher), http.StatusRequestEntityTooLarge)

	// Три сохранения — одна ждущая джоба, срок — интервал
	j := f.snapshotJobs(t)
	if j.count != 1 || j.dueIn <= 0 || j.dueIn > whiteboardSnapshotDelay {
		t.Fatalf("snapshot jobs %+v, want one due within %s", j, whiteboardSnapshotDelay)
	}

	scenes := &fakeScenes{objects: map[string][]byte{}}
	runner := jobs.NewRunner(f.a.store.Pool(), jobs.Config{WorkerID: "test-worker"})
	worker.RegisterWhiteboard(runner, f.a.store, scenes)
	runJob := func(want bool) {
		t.Helper()
		worked, err := runner.RunOnce(ctx)
		if err != nil || worked != want {
			t.Fatalf("RunOnce = %v, %v; want %v", worked, err, want)
		}
	}
	runJob(false) // срок ещё не пришёл

	// Конец урока: комната закрылась — снимок сразу, той же джобой
	webhook := func(event *livekit.WebhookEvent) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, signedWebhook(t, event))
		expectStatus(t, w, http.StatusOK)
	}
	webhook(&livekit.WebhookEvent{Event: eventRoomFinished, Room: &livekit.Room{Name: roomName(f.lessonID)}})
	if j := f.snapshotJobs(t); j.count != 1 || j.dueIn > time.Second {
		t.Fatalf("room_finished did not pull the snapshot in: %+v", j)
	}
	// У урока без доски снимать нечего
	webhook(&livekit.WebhookEvent{Event: eventRoomFinished, Room: &livekit.Room{Name: roomName(f.eventID)}})
	var eventJobs int
	if err := f.conn.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND payload = $2`,
		store.JobWhiteboardSnapshot, map[string]int64{"lesson_id": f.eventID}).Scan(&eventJobs); err != nil || eventJobs != 0 {
		t.Fatalf("snapshot queued for a lesson without a board: %d, %v", eventJobs, err)
	}

	// Хранилище недоступно — ретрай с backoff, отметки снимка нет
	scenes.err = errors.New("storage is down")
	runJob(true)
	if j := f.snapshotJobs(t); j.count != 1 || j.attempts != 1 || j.dueIn < 30*time.Second {
		t.Fatalf("failed snapshot not retried later: %+v", j)
	}
	if b := f.board(t); b.key != "" || b.snapshotVersion != 0 {
		t.Fatalf("failed snapshot marked: %+v", b)
	}

	scenes.err = nil
	if _, err := f.conn.Exec(ctx, `UPDATE jobs SET run_at = now() WHERE kind = $1`, store.JobWhiteboardSnapshot); err != nil {
		t.Fatal(err)
	}
	runJob(true)
	key := storage.WhiteboardKey(f.lessonID)
	if got := sceneTexts(t, scenes.objects[key]); got != "a@2:apples,-b@2:pear" {
		t.Fatalf("snapshot %s", got)
	}
	if b := f.board(t); b.key != key || b.snapshotVersion != b.sceneVersion {
		t.Fatalf("snapshot not marked: %+v", b)
	}
	if j := f.snapshotJobs(t); j.count != 0 {
		t.Fatalf("%d snapshot jobs left", j.count)
	}

	// at-least-once: повтор той же джобы хранилище не трогает
	if err := jobs.EnqueueOnce(ctx, f.a.store.Pool(), store.JobWhiteboardSnapshot,
		store.WhiteboardSnapshotJob{LessonID: f.lessonID}, 0); err != nil {
		t.Fatal(err)
	}
	runJob(true)
	if scenes.puts != 1 {
		t.Fatalf("repeated snapshot wrote %d objects", scenes.puts)
	}
	// Урок с доской удалён — джоба доходит до конца без ошибки
	if _, err := f.conn.Exec(ctx, `DELETE FROM whiteboards`); err != nil {
		t.Fatal(err)
	}
	if err := jobs.EnqueueOnce(ctx, f.a.store.Pool(), store.JobWhiteboardSnapshot,
		store.WhiteboardSnapshotJob{LessonID: f.lessonID}, 0); err != nil {
		t.Fatal(err)
	}
	runJob(true)
	if j := f.snapshotJobs(t); j.count != 0 {
		t.Fatalf("snapshot of a deleted board left %d jobs", j.count)
	}
}

// Миграция 0010 — модель данных плюс комната, её ключ, сцена и версии
// (ADR-020); откат убирает таблицу и ждущие снимки, повторный накат
// возвращает таблицу.
func TestWhiteboardMigrationReverts(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	columns := func() string {
		rows, err := conn.Query(ctx, `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'whiteboards' ORDER BY column_name`)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(names, ",")
	}
	want := "created_at,lesson_id,room_id,room_key,scene,scene_s3_key,scene_version,snapshot_version,updated_at"
	if got := columns(); got != want {
		t.Fatalf("columns %s, want %s", got, want)
	}
	if err := jobs.EnqueueOnce(ctx, a.store.Pool(), store.JobWhiteboardSnapshot,
		store.WhiteboardSnapshotJob{LessonID: 1}, 0); err != nil {
		t.Fatal(err)
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
	apply("../../migrations/0010_whiteboards.down.sql")
	var left int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("down left %d snapshot jobs (%v)", left, err)
	}
	if got := columns(); got != "" {
		t.Fatalf("down left columns %s", got)
	}
	apply("../../migrations/0010_whiteboards.up.sql")
	if got := columns(); got != want {
		t.Fatalf("columns after re-up %s", got)
	}
}
