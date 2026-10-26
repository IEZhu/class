package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

// Имя комнаты — единственная ниточка от вебхука к уроку, поэтому разбор
// должен быть строгим: чужая комната не должна случайно попасть в наш урок.
func TestLessonIDFromRoom(t *testing.T) {
	cases := []struct {
		room string
		want int64
		ok   bool
	}{
		{"lesson-42", 42, true},
		{"lesson-1", 1, true},
		{"lesson-0", 0, false},
		{"lesson--1", 0, false},
		{"lesson-", 0, false},
		{"lesson-abc", 0, false},
		{"lesson-1x", 0, false},
		{"other-1", 0, false},
		{"", 0, false},
		{"prefix-lesson-1", 0, false},
	}
	for _, c := range cases {
		got, ok := lessonIDFromRoom(c.room)
		if ok != c.ok || got != c.want {
			t.Errorf("lessonIDFromRoom(%q) = (%d, %v), хотели (%d, %v)", c.room, got, ok, c.want, c.ok)
		}
	}
}

// roomName и lessonIDFromRoom должны быть обратны друг другу: разъедутся —
// запись урока потеряется молча.
func TestRoomNameRoundTrip(t *testing.T) {
	for _, id := range []int64{1, 7, 42, 1000000} {
		got, ok := lessonIDFromRoom(roomName(id))
		if !ok || got != id {
			t.Errorf("round-trip для %d: получили (%d, %v)", id, got, ok)
		}
	}
}

func TestEgressRequestVideo(t *testing.T) {
	a := &API{cfg: Config{Storage: storage.Config{
		Bucket: "lingua-class", Region: "auto",
		Endpoint:  "https://acc.r2.cloudflarestorage.com",
		AccessKey: "key", SecretKey: "secret",
	}}}

	req := a.egressRequest(7, roomName(7))
	if req.RoomName != "lesson-7" {
		t.Errorf("room = %q", req.RoomName)
	}
	if req.AudioOnly {
		t.Error("AudioOnly = true, хотя режим видео")
	}
	if len(req.FileOutputs) != 1 {
		t.Fatalf("выходов %d, хотели 1", len(req.FileOutputs))
	}
	out := req.FileOutputs[0]
	if out.FileType != livekit.EncodedFileType_MP4 {
		t.Errorf("тип файла = %v, хотели MP4", out.FileType)
	}
	if want := storage.RecordingKey(7, "mp4"); out.Filepath != want {
		t.Errorf("filepath = %q, хотели %q", out.Filepath, want)
	}
	if !out.DisableManifest {
		t.Error("манифест не отключён — лишний объект рядом с записью")
	}

	s3 := out.GetS3()
	if s3 == nil {
		t.Fatal("вывод не в S3")
	}
	if s3.Bucket != "lingua-class" || s3.Region != "auto" {
		t.Errorf("бакет/регион = %q/%q", s3.Bucket, s3.Region)
	}
	if s3.AccessKey != "key" || s3.Secret != "secret" {
		t.Error("ключи хранилища не проброшены в egress")
	}
	// Для R2 обязателен path-style: без него Egress пойдёт на
	// bucket.<account>.r2.… и не попадёт в бакет
	if !s3.ForcePathStyle {
		t.Error("ForcePathStyle = false при заданном endpoint")
	}
}

func TestEgressRequestAudioOnly(t *testing.T) {
	a := &API{cfg: Config{EgressAudioOnly: true, Storage: storage.Config{Bucket: "b"}}}

	req := a.egressRequest(3, roomName(3))
	if !req.AudioOnly {
		t.Error("AudioOnly = false при включённом режиме")
	}
	out := req.FileOutputs[0]
	if out.FileType != livekit.EncodedFileType_OGG {
		t.Errorf("тип файла = %v, хотели OGG", out.FileType)
	}
	if want := storage.RecordingKey(3, "ogg"); out.Filepath != want {
		t.Errorf("filepath = %q, хотели %q", out.Filepath, want)
	}
	// Без endpoint (AWS) path-style не навязываем
	if out.GetS3().ForcePathStyle {
		t.Error("ForcePathStyle = true без endpoint")
	}
}

const (
	webhookKey    = "fixture-key"
	webhookSecret = "fixture-signing-key-at-least-32-bytes"
)

// signedWebhook — запрос так, как его шлёт LiveKit: JWT с sha256 тела.
func signedWebhook(t *testing.T, event *livekit.WebhookEvent) *http.Request {
	t.Helper()
	body, err := protojson.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	token, err := auth.NewAccessToken(webhookKey, webhookSecret).
		SetValidFor(time.Minute).
		SetSha256(base64.StdEncoding.EncodeToString(sum[:])).
		ToJWT()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/webhooks/livekit", bytes.NewReader(body))
	r.Header.Set("Authorization", token)
	r.Header.Set("Content-Type", webhook.ContentType)
	return r
}

// Ключи без URL — интеграция не настроена, как и у комнаты: иначе вебхук
// принял бы подпись и пошёл запускать Egress по пустому адресу.
func TestWebhookWithoutLiveKitURL(t *testing.T) {
	a := &API{cfg: Config{LiveKitAPIKey: webhookKey, LiveKitAPISecret: webhookSecret}}
	w := httptest.NewRecorder()
	a.Router().ServeHTTP(w, signedWebhook(t, &livekit.WebhookEvent{
		Event: eventRoomStarted, Room: &livekit.Room{Name: roomName(1)},
	}))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, хотели 503", w.Code)
	}
}

// fakeEgress — серверный API LiveKit: отвечает на StartRoomCompositeEgress
// тем, что вернёт respond.
func fakeEgress(t *testing.T, respond func(w http.ResponseWriter)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/twirp/livekit.Egress/StartRoomCompositeEgress" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		respond(w)
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func egressStarted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"egressId":"EG_fixture"}`))
}

// recordingAPI — api с настроенными LiveKit (адрес — fake) и хранилищем
// поверх изолированной схемы securityDB, плюс урок в статусе scheduled.
func recordingAPI(t *testing.T, egressURL string) (http.Handler, *pgx.Conn, int64) {
	t.Helper()
	base, conn := securityDB(t)
	ctx := context.Background()
	teacherID, _ := securityUser(t, base, conn, store.RoleTeacher, "rec-teacher")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('rec','B1') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	lesson, err := base.store.CreateLesson(ctx, &groupID, "", teacherID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(base.store, Config{
		LiveKitURL: egressURL, LiveKitAPIKey: webhookKey, LiveKitAPISecret: webhookSecret,
		Storage: storage.Config{
			Bucket: "lingua-class", Endpoint: "https://acc.r2.cloudflarestorage.com",
			AccessKey: "key", SecretKey: "secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a.Router(), conn, lesson.ID
}

func lessonStatus(t *testing.T, conn *pgx.Conn, id int64) string {
	t.Helper()
	var status string
	if err := conn.QueryRow(context.Background(), `SELECT status FROM lessons WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// Сорванный запуск Egress не должен оставлять урок без записи навсегда:
// урок остаётся scheduled, LiveKit получает 5xx и повторяет доставку,
// а следующий participant_joined тоже запускает запись.
func TestRecordingRetriesAfterEgressFailure(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	srv, calls := fakeEgress(t, func(w http.ResponseWriter) {
		if down.Load() {
			http.Error(w, `{"code":"unavailable","msg":"egress down"}`, http.StatusServiceUnavailable)
			return
		}
		egressStarted(w)
	})
	h, conn, lessonID := recordingAPI(t, srv.URL)
	room := &livekit.Room{Name: roomName(lessonID)}
	deliver := func(event string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, signedWebhook(t, &livekit.WebhookEvent{Event: event, Room: room}))
		return w.Code
	}
	step := func(name, event string, wantCode int, wantCalls int32, wantStatus string) {
		t.Helper()
		if code := deliver(event); code != wantCode {
			t.Errorf("%s: ответ %d, хотели %d", name, code, wantCode)
		}
		if got := calls.Load(); got != wantCalls {
			t.Errorf("%s: запусков Egress %d, хотели %d", name, got, wantCalls)
		}
		if got := lessonStatus(t, conn, lessonID); got != wantStatus {
			t.Errorf("%s: статус %q, хотели %q", name, got, wantStatus)
		}
	}

	step("room_started, Egress упал", eventRoomStarted, http.StatusServiceUnavailable, 1, "scheduled")
	step("повтор room_started, Egress всё ещё лежит", eventRoomStarted, http.StatusServiceUnavailable, 2, "scheduled")
	down.Store(false)
	step("participant_joined после сбоя", eventParticipantJoined, http.StatusOK, 3, "live")
	step("повтор room_started при идущей записи", eventRoomStarted, http.StatusOK, 3, "live")
	step("ещё participant_joined", eventParticipantJoined, http.StatusOK, 3, "live")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedWebhook(t, &livekit.WebhookEvent{Event: eventEgressEnded, EgressInfo: &livekit.EgressInfo{
		EgressId: "EG_fixture", RoomName: room.Name, Status: livekit.EgressStatus_EGRESS_COMPLETE,
		FileResults: []*livekit.FileInfo{{Filename: storage.RecordingKey(lessonID, "mp4"), Size: 1}},
	}}))
	// S1-2: egress_ended ставит processing и джобу recording_ready, done ставит worker
	if w.Code != http.StatusOK || lessonStatus(t, conn, lessonID) != "processing" {
		t.Errorf("egress_ended: ответ %d, статус %q", w.Code, lessonStatus(t, conn, lessonID))
	}
}

// Две одновременные доставки (room_started и participant_joined первого
// участника приходят почти вместе) запускают один Egress, и вторая
// не ждёт, пока первая договорится с LiveKit.
func TestRecordingStartsOnce(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var n atomic.Int32
	srv, calls := fakeEgress(t, func(w http.ResponseWriter) {
		if n.Add(1) == 1 {
			close(entered)
			<-release
		}
		egressStarted(w)
	})
	h, conn, lessonID := recordingAPI(t, srv.URL)
	room := &livekit.Room{Name: roomName(lessonID)}
	deliver := func(event string) <-chan int {
		r := signedWebhook(t, &livekit.WebhookEvent{Event: event, Room: room})
		done := make(chan int, 1)
		go func() {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			done <- w.Code
		}()
		return done
	}

	first := deliver(eventRoomStarted)
	select {
	case <-entered:
	case code := <-first:
		t.Fatalf("первая доставка не дошла до Egress: ответ %d", code)
	}
	select {
	case code := <-deliver(eventParticipantJoined):
		if code != http.StatusOK {
			t.Errorf("параллельная доставка: ответ %d, хотели 200", code)
		}
	case <-time.After(5 * time.Second):
		t.Error("параллельная доставка ждёт первую, а должна пропустить урок")
	}
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Errorf("первая доставка: ответ %d, хотели 200", code)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("запусков Egress %d, хотели 1", got)
	}
	if got := lessonStatus(t, conn, lessonID); got != "live" {
		t.Errorf("статус %q, хотели live", got)
	}
}
