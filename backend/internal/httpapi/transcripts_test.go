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
}

func newASRStand(t *testing.T, maxAttempts int) *asrStand {
	t.Helper()
	base, conn := securityDB(t)
	ctx := context.Background()
	s := &asrStand{conn: conn, st: base.store, asr: newFakeASR(t), files: storagetest.New(t)}
	teacherID, _ := securityUser(t, base, conn, store.RoleTeacher, "asr-teacher")
	studentID, _ := securityUser(t, base, conn, store.RoleStudent, "asr-student")
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
		Storage: s.files.Config(),
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
