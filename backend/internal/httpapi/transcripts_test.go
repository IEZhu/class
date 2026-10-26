package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/livekit/protocol/livekit"

	"github.com/IEZhu/class/backend/internal/assemblyai"
	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/storage/storagetest"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/worker"
)

const (
	asrKey        = "fixture-assemblyai-key"
	asrSecret     = "fixture-webhook-secret-0123456789abcdef"
	asrExternalID = "tr_fixture"
)

// fakeASR — REST API AssemblyAI v2: принимает отправку и отдаёт результат,
// заданный тестом.
type fakeASR struct {
	srv *httptest.Server

	mu           sync.Mutex
	submits      []map[string]any
	gets         int
	submitStatus int    // не 0 — отправка отвечает этим кодом
	result       string // тело GET /v2/transcript/{id}
}

func newFakeASR(t *testing.T) *fakeASR {
	t.Helper()
	f := &fakeASR{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != asrKey {
			http.Error(w, `{"error":"Authentication error, API token missing/invalid"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/transcript":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
				return
			}
			f.submits = append(f.submits, body)
			if f.submitStatus != 0 {
				http.Error(w, `{"error":"service unavailable"}`, f.submitStatus)
				return
			}
			_, _ = w.Write([]byte(`{"id":"` + asrExternalID + `","status":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/transcript/"+asrExternalID:
			f.gets++
			_, _ = w.Write([]byte(f.result))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeASR) calls() (submits, gets int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.submits), f.gets
}

// respond задаёт ответы: тело забора результата и код отправки (0 — 200).
func (f *fakeASR) respond(result string, submitStatus int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result, f.submitStatus = result, submitStatus
}

func (f *fakeASR) firstSubmit() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.submits[0]
}

// Результат AssemblyAI: три реплики двух спикеров, в одной — разметка,
// которую VTT обязан экранировать.
const asrCompleted = `{"id":"tr_fixture","status":"completed","text":"...","utterances":[
	{"speaker":"A","start":250,"end":2100,"confidence":0.9,"text":"Good morning, class.","words":[]},
	{"speaker":"B","start":2300,"end":3900,"confidence":0.8,"text":"Morning! Is it <b>today</b> & now?","words":[]},
	{"speaker":"A","start":4000,"end":3725120,"confidence":0.9,"text":"Yes.","words":[]}]}`

const asrCompletedVTT = "WEBVTT\n" +
	"\n00:00:00.250 --> 00:00:02.100\n<v A>Good morning, class.\n" +
	"\n00:00:02.300 --> 00:00:03.900\n<v B>Morning! Is it &lt;b&gt;today&lt;/b&gt; &amp; now?\n" +
	"\n00:00:04.000 --> 01:02:05.120\n<v A>Yes.\n"

// asrStand — api и worker с fake AssemblyAI и fake хранилищем поверх
// изолированной схемы securityDB; урок в live, в группе — студент.
type asrStand struct {
	h        http.Handler
	conn     *pgx.Conn
	st       *store.Store
	runner   *jobs.Runner
	asr      *fakeASR
	files    *storagetest.Server
	lessonID int64
	// Сессии: участник, teacher урока, админ, чужие студент и teacher
	student, teacher, admin, outsider, otherTeacher *http.Cookie
}

func newASRStand(t *testing.T, maxAttempts int) *asrStand {
	t.Helper()
	base, conn := securityDB(t)
	ctx := context.Background()
	s := &asrStand{conn: conn, st: base.store, asr: newFakeASR(t), files: storagetest.New(t)}
	teacherID, teacher := securityUser(t, base, conn, store.RoleTeacher, "asr-teacher")
	studentID, student := securityUser(t, base, conn, store.RoleStudent, "asr-student")
	_, s.admin = securityUser(t, base, conn, store.RoleAdmin, "asr-admin")
	_, s.outsider = securityUser(t, base, conn, store.RoleStudent, "asr-outsider")
	_, s.otherTeacher = securityUser(t, base, conn, store.RoleTeacher, "asr-other-teacher")
	s.student, s.teacher = student, teacher
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('asr','B1') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, groupID, studentID); err != nil {
		t.Fatal(err)
	}
	lesson, err := base.store.CreateLesson(ctx, &groupID, "", teacherID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	s.lessonID = lesson.ID
	if _, err := conn.Exec(ctx, `UPDATE lessons SET status = 'live' WHERE id = $1`, lesson.ID); err != nil {
		t.Fatal(err)
	}

	a, err := New(base.store, Config{
		PublicBaseURL: "https://class.example",
		LiveKitURL:    "wss://rooms.example", LiveKitAPIKey: webhookKey, LiveKitAPISecret: webhookSecret,
		Storage:                 s.files.Config(),
		AssemblyAIWebhookSecret: asrSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.h = a.Router()

	client, err := assemblyai.New(assemblyai.Config{APIKey: asrKey, WebhookSecret: asrSecret, BaseURL: s.asr.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	files, err := storage.New(s.files.Config())
	if err != nil {
		t.Fatal(err)
	}
	s.runner = jobs.NewRunner(base.store.Pool(), jobs.Config{WorkerID: "asr-worker", MaxAttempts: maxAttempts})
	worker.Register(s.runner, base.store, worker.ASR{
		Client: client, Storage: files, WebhookURL: "https://class.example/api/webhooks/assemblyai",
	})
	return s
}

// egressEnded — LiveKit дописал запись урока.
func (s *asrStand) egressEnded(t *testing.T) {
	t.Helper()
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, signedWebhook(t, &livekit.WebhookEvent{Event: eventEgressEnded, EgressInfo: &livekit.EgressInfo{
		RoomName:    roomName(s.lessonID),
		Status:      livekit.EgressStatus_EGRESS_COMPLETE,
		FileResults: []*livekit.FileInfo{{Filename: storage.RecordingKey(s.lessonID, "mp4"), Size: 1 << 20}},
	}}))
	expectStatus(t, w, http.StatusOK)
}

// webhook — доставка AssemblyAI с секретом secret ("" — без заголовка).
func (s *asrStand) webhook(secret, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/assemblyai", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if secret != "" {
		r.Header.Set(assemblyai.WebhookHeader, secret)
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	return w
}

const asrCompletedHook = `{"transcript_id":"tr_fixture","status":"completed"}`

// run выполняет одну джобу; want — kind живых джоб до запуска, по порядку.
func (s *asrStand) run(t *testing.T, want ...string) {
	t.Helper()
	s.expectQueue(t, want...)
	if worked, err := s.runner.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("RunOnce = %v, %v", worked, err)
	}
}

func (s *asrStand) expectQueue(t *testing.T, want ...string) {
	t.Helper()
	rows, err := s.conn.Query(context.Background(),
		`SELECT kind FROM jobs WHERE failed_at IS NULL ORDER BY run_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	kinds, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("очередь %v, хотели %v", kinds, want)
	}
}

type transcriptRow struct {
	id                     int64
	status                 string
	externalID, vtt, cause *string
	utterances             int
}

func (s *asrStand) transcript(t *testing.T) transcriptRow {
	t.Helper()
	var r transcriptRow
	err := s.conn.QueryRow(context.Background(),
		`SELECT t.id, t.status, t.external_id, t.vtt_s3_key, t.error,
		        (SELECT count(*) FROM utterances u WHERE u.transcript_id = t.id)
		 FROM transcripts t WHERE t.lesson_id = $1`, s.lessonID).
		Scan(&r.id, &r.status, &r.externalID, &r.vtt, &r.cause, &r.utterances)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// submitted — урок с принятой записью доходит до отправки в AssemblyAI.
func (s *asrStand) submitted(t *testing.T) {
	t.Helper()
	s.egressEnded(t)
	s.run(t, store.JobRecordingReady)
	s.run(t, store.JobASRSubmit)
}

// fetched — после отправки AssemblyAI сообщает итог, worker забирает его.
func (s *asrStand) fetched(t *testing.T) {
	t.Helper()
	s.submitted(t)
	expectStatus(t, s.webhook(asrSecret, asrCompletedHook), http.StatusOK)
	s.run(t, store.JobASRResult)
}

// requeue ставит джобу kind ещё раз — так выглядит at-least-once повтор.
func (s *asrStand) requeue(t *testing.T, kind string, payload any) {
	t.Helper()
	if _, err := jobs.Enqueue(context.Background(), s.st.Pool(), kind, payload); err != nil {
		t.Fatal(err)
	}
}

// S2-1: после egress_ended запись сама уходит в AssemblyAI — presigned GET
// на сутки, диаризация, английский, вебхук с секретом. Повтор джоб
// не заводит второй транскрипт и не шлёт запись ещё раз.
func TestASRSubmit(t *testing.T) {
	s := newASRStand(t, 0)
	ctx := context.Background()

	s.egressEnded(t)
	s.run(t, store.JobRecordingReady)
	tr := s.transcript(t)
	if tr.status != store.TranscriptQueued {
		t.Fatalf("после recording_ready транскрипт %+v", tr)
	}
	var linked *int64
	if err := s.conn.QueryRow(ctx, `SELECT transcript_id FROM lessons WHERE id=$1`, s.lessonID).Scan(&linked); err != nil ||
		linked == nil || *linked != tr.id {
		t.Fatalf("lessons.transcript_id = %v, хотели %d (%v)", linked, tr.id, err)
	}

	s.run(t, store.JobASRSubmit)
	if submits, _ := s.asr.calls(); submits != 1 {
		t.Fatalf("отправок %d, хотели 1", submits)
	}
	body := s.asr.firstSubmit()
	for field, want := range map[string]any{
		"speaker_labels": true, "punctuate": true, "language_code": "en",
		"webhook_url":              "https://class.example/api/webhooks/assemblyai",
		"webhook_auth_header_name": assemblyai.WebhookHeader, "webhook_auth_header_value": asrSecret,
	} {
		if body[field] != want {
			t.Errorf("%s = %v, хотели %v", field, body[field], want)
		}
	}
	if models := fmt.Sprint(body["speech_models"]); models != "[universal-2]" {
		t.Errorf("speech_models = %s", models)
	}
	audio, err := url.Parse(fmt.Sprint(body["audio_url"]))
	if err != nil {
		t.Fatal(err)
	}
	if want := "/" + storagetest.Bucket + "/" + storage.RecordingKey(s.lessonID, "mp4"); audio.Path != want ||
		audio.Query().Get("X-Amz-Expires") != "86400" || audio.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("audio_url = %s", audio)
	}
	tr = s.transcript(t)
	if tr.status != store.TranscriptSubmitted || tr.externalID == nil || *tr.externalID != asrExternalID {
		t.Fatalf("после отправки %+v", tr)
	}
	if got := lessonStatus(t, s.conn, s.lessonID); got != "processing" {
		t.Fatalf("урок %s до транскрипта", got)
	}
	s.expectQueue(t)

	s.requeue(t, store.JobRecordingReady, store.RecordingReady{LessonID: s.lessonID})
	s.run(t, store.JobRecordingReady)
	s.requeue(t, store.JobASRSubmit, store.TranscriptJob{TranscriptID: tr.id})
	s.run(t, store.JobASRSubmit)
	s.expectQueue(t)
	var transcripts int
	if err := s.conn.QueryRow(ctx, `SELECT count(*) FROM transcripts`).Scan(&transcripts); err != nil || transcripts != 1 {
		t.Fatalf("транскриптов %d (%v)", transcripts, err)
	}
	if submits, _ := s.asr.calls(); submits != 1 {
		t.Fatalf("после повторов отправок %d, хотели 1", submits)
	}
}

// S2-2: вебхук проверяет секрет и принимает итог один раз; реплики
// сохраняются один раз, сколько бы ни повторялись доставка и джоба.
func TestASRWebhookStoresUtterancesOnce(t *testing.T) {
	s := newASRStand(t, 0)
	ctx := context.Background()
	s.asr.respond(asrCompleted, 0)
	s.submitted(t)

	expectStatus(t, s.webhook("", asrCompletedHook), http.StatusUnauthorized)
	expectStatus(t, s.webhook(asrSecret+"x", asrCompletedHook), http.StatusUnauthorized)
	expectStatus(t, s.webhook(strings.ToUpper(asrSecret), asrCompletedHook), http.StatusUnauthorized)
	s.expectQueue(t)
	expectStatus(t, s.webhook(asrSecret, `{"transcript_id":"tr_other","status":"completed"}`), http.StatusNotFound)
	expectStatus(t, s.webhook(asrSecret, `{"transcript_id":"tr_fixture","status":"processing"}`), http.StatusBadRequest)
	expectStatus(t, s.webhook(asrSecret, `{"transcript_id":`), http.StatusBadRequest)
	s.expectQueue(t)
	expectStatus(t, s.webhook(asrSecret, asrCompletedHook), http.StatusOK)
	expectStatus(t, s.webhook(asrSecret, asrCompletedHook), http.StatusOK) // повторная доставка
	s.run(t, store.JobASRResult)                                           // джоба одна на две доставки
	tr := s.transcript(t)
	if tr.status != store.TranscriptCompleted || tr.utterances != 3 {
		t.Fatalf("после asr_result %+v", tr)
	}
	expectStatus(t, s.webhook(asrSecret, asrCompletedHook), http.StatusOK) // запоздалая доставка
	s.expectQueue(t, store.JobVTTRender)
	if _, gets := s.asr.calls(); gets != 1 {
		t.Fatalf("результат забран %d раз, хотели 1", gets)
	}
	if tr = s.transcript(t); tr.utterances != 3 {
		t.Fatalf("после запоздалой доставки %+v", tr)
	}
	// Схема держит идемпотентность и сама: вторую реплику с тем же idx не вставить
	_, err := s.conn.Exec(ctx, `INSERT INTO utterances (transcript_id, idx, speaker_label, start_ms, end_ms, text)
		VALUES ($1, 0, 'A', 0, 1, 'dup')`, tr.id)
	if store.PgErrorCode(err) != pgUniqueViolation {
		t.Fatalf("дубль реплики: %v, хотели unique violation", err)
	}

	// Вьюер получает реплики по порядку, как их отдал AssemblyAI
	w := securityRequest(s.h, "GET", fmt.Sprintf("/lessons/%d/transcript", s.lessonID), "", s.student)
	expectStatus(t, w, http.StatusOK)
	var resp transcriptResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != store.TranscriptCompleted || len(resp.Utterances) != 3 {
		t.Fatalf("ответ %s", w.Body.String())
	}
	if u := resp.Utterances[1]; u.Idx != 1 || u.SpeakerLabel != "B" || u.StartMS != 2300 || u.EndMS != 3900 ||
		u.Text != "Morning! Is it <b>today</b> & now?" || u.UserID != nil {
		t.Errorf("реплика 1 = %+v", u)
	}
	if strings.Contains(w.Body.String(), "error") {
		t.Errorf("в ответе вьюеру есть поле ошибки: %s", w.Body.String())
	}
}

// GET /lessons/{id}/transcript пускает ровно тех же, кого GET /lessons/{id}.
func TestTranscriptAccessMatchesLesson(t *testing.T) {
	s := newASRStand(t, 0)
	s.asr.respond(asrCompleted, 0)
	s.egressEnded(t)
	s.run(t, store.JobRecordingReady)

	lesson := fmt.Sprintf("/lessons/%d", s.lessonID)
	cases := []struct {
		name   string
		cookie *http.Cookie
		want   int
	}{
		{"участник", s.student, http.StatusOK},
		{"teacher урока", s.teacher, http.StatusOK},
		{"админ", s.admin, http.StatusForbidden},
		{"чужой студент", s.outsider, http.StatusForbidden},
		{"чужой teacher", s.otherTeacher, http.StatusForbidden},
		{"без входа", nil, http.StatusUnauthorized},
	}
	for _, c := range cases {
		page := securityRequest(s.h, "GET", lesson, "", c.cookie).Code
		got := securityRequest(s.h, "GET", lesson+"/transcript", "", c.cookie)
		if page != c.want || got.Code != c.want {
			t.Errorf("%s: урок %d, транскрипт %d, хотели оба %d (%s)", c.name, page, got.Code, c.want, got.Body.String())
		}
	}
	// Транскрипт ещё готовится: статус есть, реплик и ссылки нет
	w := securityRequest(s.h, "GET", lesson+"/transcript", "", s.teacher)
	if body := strings.TrimSpace(w.Body.String()); body != `{"status":"queued","utterances":[],"vtt_url":null,"vtt_expires_at":null}` {
		t.Errorf("готовящийся транскрипт: %s", body)
	}
	for _, path := range []string{"/lessons/999999/transcript", "/lessons/999999"} {
		expectStatus(t, securityRequest(s.h, "GET", path, "", s.teacher), http.StatusNotFound)
	}
	expectStatus(t, securityRequest(s.h, "GET", "/lessons/0/transcript", "", s.teacher), http.StatusBadRequest)

	// Урок без транскрипта (ASR выключен или записи не было) — 404 своим кодом
	if _, err := s.conn.Exec(context.Background(), `DELETE FROM transcripts`); err != nil {
		t.Fatal(err)
	}
	w = securityRequest(s.h, "GET", lesson+"/transcript", "", s.student)
	expectStatus(t, w, http.StatusNotFound)
	if !strings.Contains(w.Body.String(), "no_transcript") {
		t.Errorf("тело %s", w.Body.String())
	}
}

// Ошибка у AssemblyAI — итог (ADR-016): транскрипт error с причиной,
// урок done, реплик и VTT нет, повторов нет.
func TestASRProviderErrorFinishesLesson(t *testing.T) {
	s := newASRStand(t, 0)
	s.asr.respond(`{"id":"tr_fixture","status":"error","error":"Transcoding failed. File does not appear to contain audio.","utterances":null}`, 0)
	s.egressEnded(t)
	s.run(t, store.JobRecordingReady)
	s.run(t, store.JobASRSubmit)
	expectStatus(t, s.webhook(asrSecret, `{"transcript_id":"tr_fixture","status":"error"}`), http.StatusOK)
	s.run(t, store.JobASRResult)
	s.expectQueue(t)
	tr := s.transcript(t)
	if tr.status != store.TranscriptError || tr.cause == nil ||
		*tr.cause != "AssemblyAI: Transcoding failed. File does not appear to contain audio." || tr.utterances != 0 || tr.vtt != nil {
		t.Fatalf("транскрипт %+v", tr)
	}
	if got := lessonStatus(t, s.conn, s.lessonID); got != "done" {
		t.Fatalf("урок %s, хотели done без транскрипта", got)
	}
	w := securityRequest(s.h, "GET", fmt.Sprintf("/lessons/%d/transcript", s.lessonID), "", s.student)
	if body := strings.TrimSpace(w.Body.String()); w.Code != http.StatusOK ||
		body != `{"status":"error","utterances":[],"vtt_url":null,"vtt_expires_at":null}` {
		t.Fatalf("%d %s", w.Code, body)
	}
}

// Недоступный AssemblyAI: повтор с backoff очереди, а на последней попытке
// транскрибация сдаётся — урок done, джоба не остаётся мёртвой (ADR-016).
func TestASRSubmitRetriesThenGivesUp(t *testing.T) {
	s := newASRStand(t, 2)
	s.asr.respond("", http.StatusServiceUnavailable)
	s.egressEnded(t)
	s.run(t, store.JobRecordingReady)

	s.run(t, store.JobASRSubmit)
	var lastError string
	if err := s.conn.QueryRow(context.Background(),
		`SELECT last_error FROM jobs WHERE kind = $1`, store.JobASRSubmit).Scan(&lastError); err != nil ||
		!strings.Contains(lastError, "503") {
		t.Fatalf("last_error = %q (%v)", lastError, err)
	}
	if tr := s.transcript(t); tr.status != store.TranscriptQueued || tr.cause != nil {
		t.Fatalf("после первой неудачи %+v", tr)
	}
	if got := lessonStatus(t, s.conn, s.lessonID); got != "processing" {
		t.Fatalf("урок %s после первой неудачи", got)
	}
	// Проматываем backoff
	if _, err := s.conn.Exec(context.Background(), `UPDATE jobs SET run_at = now()`); err != nil {
		t.Fatal(err)
	}
	s.run(t, store.JobASRSubmit)
	s.expectQueue(t)
	var dead int
	if err := s.conn.QueryRow(context.Background(), `SELECT count(*) FROM jobs`).Scan(&dead); err != nil || dead != 0 {
		t.Fatalf("в очереди осталось %d джоб (%v)", dead, err)
	}
	tr := s.transcript(t)
	if tr.status != store.TranscriptError || tr.cause == nil || !strings.Contains(*tr.cause, "503") || tr.externalID != nil {
		t.Fatalf("после последней попытки %+v", tr)
	}
	if got := lessonStatus(t, s.conn, s.lessonID); got != "done" {
		t.Fatalf("урок %s после отказа ASR", got)
	}
	if submits, _ := s.asr.calls(); submits != 2 {
		t.Fatalf("отправок %d, хотели 2", submits)
	}
}

// Хранилище не приняло VTT: реплики уже в базе и остаются, транскрипт
// completed без VTT, причина в error, урок done.
func TestVTTUploadFailureKeepsUtterances(t *testing.T) {
	s := newASRStand(t, 1)
	s.asr.respond(asrCompleted, 0)
	s.egressEnded(t)
	s.run(t, store.JobRecordingReady)
	s.run(t, store.JobASRSubmit)
	expectStatus(t, s.webhook(asrSecret, asrCompletedHook), http.StatusOK)
	s.run(t, store.JobASRResult)
	s.files.Fail(true)
	s.run(t, store.JobVTTRender)
	s.expectQueue(t)
	tr := s.transcript(t)
	if tr.status != store.TranscriptCompleted || tr.utterances != 3 || tr.vtt != nil || tr.cause == nil {
		t.Fatalf("транскрипт %+v", tr)
	}
	if got := lessonStatus(t, s.conn, s.lessonID); got != "done" {
		t.Fatalf("урок %s", got)
	}
	// Перерендер (S2-4) после починки хранилища выкладывает VTT и забывает сбой
	s.files.Fail(false)
	if _, err := jobs.Enqueue(context.Background(), s.st.Pool(), store.JobVTTRender, store.TranscriptJob{TranscriptID: tr.id}); err != nil {
		t.Fatal(err)
	}
	s.run(t, store.JobVTTRender)
	if tr = s.transcript(t); tr.vtt == nil || tr.cause != nil {
		t.Fatalf("после перерендера %+v", tr)
	}
}

// Ключ убрали, пока транскрипт стоял в очереди: джоба не висит вечно,
// а доводит урок до done.
func TestASRJobWithoutKeyFinishesLesson(t *testing.T) {
	s := newASRStand(t, 0)
	s.egressEnded(t)
	s.run(t, store.JobRecordingReady)
	bare := jobs.NewRunner(s.st.Pool(), jobs.Config{WorkerID: "bare-worker"})
	worker.Register(bare, s.st, worker.ASR{})
	if worked, err := bare.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("RunOnce = %v, %v", worked, err)
	}
	tr := s.transcript(t)
	if tr.status != store.TranscriptError || tr.cause == nil || !strings.Contains(*tr.cause, "ASSEMBLYAI_API_KEY") {
		t.Fatalf("транскрипт %+v", tr)
	}
	if got := lessonStatus(t, s.conn, s.lessonID); got != "done" {
		t.Fatalf("урок %s", got)
	}
	if submits, _ := s.asr.calls(); submits != 0 {
		t.Fatalf("без ключа ушло %d отправок", submits)
	}
}

// Без секрета вебхук выключен, как комната без ключей LiveKit; короткий
// секрет не даёт api стартовать.
func TestAssemblyAIWebhookConfig(t *testing.T) {
	a := &API{cfg: Config{}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/webhooks/assemblyai", strings.NewReader(asrCompletedHook))
	r.Header.Set(assemblyai.WebhookHeader, "")
	a.Router().ServeHTTP(w, r)
	expectStatus(t, w, http.StatusServiceUnavailable)

	if _, err := New(nil, Config{AssemblyAIWebhookSecret: "short-secret"}); err == nil {
		t.Fatal("короткий секрет вебхука принят")
	}
	if _, err := New(nil, Config{AssemblyAIWebhookSecret: asrSecret}); err != nil {
		t.Fatalf("годный секрет: %v", err)
	}
}

// 0008 по модели данных; откат убирает таблицы и FK, повторный накат
// возвращает их (на живом golang-migrate — в Факте S2-1).
func TestTranscriptsMigrationReverts(t *testing.T) {
	_, conn := securityDB(t)
	ctx := context.Background()
	columns := func(table string) string {
		rows, err := conn.Query(ctx, `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position`, table)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(names, ",")
	}
	check := func() {
		t.Helper()
		if got := columns("transcripts"); got != "id,lesson_id,provider,status,external_id,vtt_s3_key,error,created_at" {
			t.Fatalf("transcripts: %s", got)
		}
		if got := columns("utterances"); got != "transcript_id,idx,speaker_label,user_id,start_ms,end_ms,text" {
			t.Fatalf("utterances: %s", got)
		}
	}
	apply := func(file string) {
		t.Helper()
		sql, err := os.ReadFile("../../migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	check()
	apply("0008_transcripts.down.sql")
	if got := columns("transcripts") + columns("utterances"); got != "" {
		t.Fatalf("после отката остались колонки %s", got)
	}
	var fk bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'lessons_transcript_fk')`).Scan(&fk); err != nil || fk {
		t.Fatalf("FK lessons.transcript_id после отката: %v (%v)", fk, err)
	}
	apply("0008_transcripts.up.sql")
	check()
}

// Полная цепочка S2-1…S2-3: egress_ended → recording_ready → asr_submit →
// вебхук → asr_result → vtt_render → VTT в хранилище, урок done. Повторы
// джоб после этого ничего не меняют.
func TestASRPipeline(t *testing.T) {
	s := newASRStand(t, 0)
	s.asr.respond(asrCompleted, 0)
	s.fetched(t)

	s.run(t, store.JobVTTRender)
	key := storage.TranscriptKey(s.lessonID)
	obj, ok := s.files.Object(key)
	if !ok || string(obj.Body) != asrCompletedVTT || obj.ContentType != "text/vtt; charset=utf-8" {
		t.Fatalf("VTT в хранилище (%v): %q %q", ok, obj.ContentType, obj.Body)
	}
	tr := s.transcript(t)
	if tr.vtt == nil || *tr.vtt != key || tr.cause != nil {
		t.Fatalf("после vtt_render %+v", tr)
	}
	if got := lessonStatus(t, s.conn, s.lessonID); got != "done" {
		t.Fatalf("урок %s после VTT", got)
	}
	s.expectQueue(t)

	// at-least-once: повтор каждой джобы ничего не задваивает, не шлёт
	// запись в AssemblyAI ещё раз и не возвращает урок из done
	s.requeue(t, store.JobRecordingReady, store.RecordingReady{LessonID: s.lessonID})
	s.run(t, store.JobRecordingReady)
	for _, kind := range []string{store.JobASRSubmit, store.JobASRResult, store.JobVTTRender} {
		s.requeue(t, kind, store.TranscriptJob{TranscriptID: tr.id})
		s.run(t, kind)
	}
	s.expectQueue(t)
	if submits, gets := s.asr.calls(); submits != 1 || gets != 1 {
		t.Fatalf("к AssemblyAI: %d отправок, %d забора; хотели по одному", submits, gets)
	}
	if tr = s.transcript(t); tr.status != store.TranscriptCompleted || tr.utterances != 3 || tr.vtt == nil {
		t.Fatalf("после повторов %+v", tr)
	}
	expectStatus(t, s.webhook(asrSecret, asrCompletedHook), http.StatusOK) // запоздалая доставка
	s.expectQueue(t)
	if got := lessonStatus(t, s.conn, s.lessonID); got != "done" {
		t.Fatalf("урок %s после повторов", got)
	}

	// Вьюер получает presigned-ссылку на этот VTT, как на запись — на 30 мин
	w := securityRequest(s.h, "GET", fmt.Sprintf("/lessons/%d/transcript", s.lessonID), "", s.student)
	expectStatus(t, w, http.StatusOK)
	var resp transcriptResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.VTTURL == nil || resp.VTTExpiresAt == nil {
		t.Fatalf("ответ %s", w.Body.String())
	}
	vtt, err := url.Parse(*resp.VTTURL)
	if err != nil || vtt.Path != "/"+storagetest.Bucket+"/"+key || vtt.Query().Get("X-Amz-Expires") != "1800" {
		t.Errorf("vtt_url = %s (%v)", *resp.VTTURL, err)
	}
}
