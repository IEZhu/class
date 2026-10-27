package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/testdb"
	"github.com/IEZhu/class/backend/internal/worker"
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
		// ParseInt принимает знак и ведущие нули — каноническая форма их нет
		{"lesson-+5", 0, false},
		{"lesson-05", 0, false},
		{"lesson-00", 0, false},
		{"lesson- 5", 0, false},
		{"lesson-99999999999999999999", 0, false}, // за пределом int64
		{"lesson-9223372036854775807", 9223372036854775807, true},
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

// segmentKey — раскладка ключа сегмента записи (ADR-031).
func segmentKey(lessonID int64, ext string) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`^recordings/%d/\d{8}T\d{6}Z-[0-9a-f]{16}\.%s$`, lessonID, ext))
}

func TestEgressRequestVideo(t *testing.T) {
	a := &API{cfg: Config{Storage: storage.Config{
		Bucket: "lingua-class", Region: "auto",
		Endpoint:  "https://acc.r2.cloudflarestorage.com",
		AccessKey: "key", SecretKey: "secret",
	}}}

	req, err := a.egressRequest(7, roomName(7))
	if err != nil {
		t.Fatal(err)
	}
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
	if !segmentKey(7, "mp4").MatchString(out.Filepath) {
		t.Errorf("filepath = %q не по раскладке сегментов", out.Filepath)
	}
	again, err := a.egressRequest(7, roomName(7))
	if err != nil {
		t.Fatal(err)
	}
	if again.FileOutputs[0].Filepath == out.Filepath {
		t.Error("второй запуск пишет в тот же ключ — перезапишет первую запись")
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

	req, err := a.egressRequest(3, roomName(3))
	if err != nil {
		t.Fatal(err)
	}
	if !req.AudioOnly {
		t.Error("AudioOnly = false при включённом режиме")
	}
	out := req.FileOutputs[0]
	if out.FileType != livekit.EncodedFileType_OGG {
		t.Errorf("тип файла = %v, хотели OGG", out.FileType)
	}
	if !segmentKey(3, "ogg").MatchString(out.Filepath) {
		t.Errorf("filepath = %q не по раскладке сегментов", out.Filepath)
	}
	// Без endpoint (AWS) path-style не навязываем
	if out.GetS3().ForcePathStyle {
		t.Error("ForcePathStyle = true без endpoint")
	}
}

// Egress получает тот регион, которым api подписывает ссылки: пустой
// S3_REGION у R2 — это auto, а не пустая строка в запросе к LiveKit;
// AWS без региона (или с auto из шаблона) api не стартует вовсе.
func TestEgressRequestUsesResolvedRegion(t *testing.T) {
	r2 := storage.Config{Bucket: "b", Endpoint: "https://acc.r2.cloudflarestorage.com", AccessKey: "k", SecretKey: "s"}
	a, err := New(nil, Config{Storage: r2})
	if err != nil {
		t.Fatal(err)
	}
	req, err := a.egressRequest(1, roomName(1))
	if err != nil {
		t.Fatal(err)
	}
	if got := req.FileOutputs[0].GetS3().Region; got != "auto" {
		t.Errorf("регион Egress у R2 с пустым S3_REGION = %q, хотели auto", got)
	}
	for _, region := range []string{"", "auto"} {
		aws := storage.Config{Bucket: "b", Region: region, AccessKey: "k", SecretKey: "s"}
		if _, err := New(nil, Config{Storage: aws}); err == nil {
			t.Errorf("AWS с регионом %q: api стартовал", region)
		}
	}
}

const (
	webhookKey    = "fixture-key"
	webhookSecret = "fixture-signing-key-at-least-32-bytes"
)

func webhookBody(t *testing.T, event *livekit.WebhookEvent) []byte {
	t.Helper()
	body, err := protojson.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// webhookToken — Authorization так, как его ставит LiveKit: JWT с sha256 тела.
func webhookToken(t *testing.T, secret string, body []byte) string {
	t.Helper()
	sum := sha256.Sum256(body)
	token, err := auth.NewAccessToken(webhookKey, secret).
		SetValidFor(time.Minute).
		SetSha256(base64.StdEncoding.EncodeToString(sum[:])).
		ToJWT()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func webhookRequest(body []byte, authorization string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/livekit", bytes.NewReader(body))
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	r.Header.Set("Content-Type", webhook.ContentType)
	return r
}

// signedWebhook — запрос так, как его шлёт LiveKit.
func signedWebhook(t *testing.T, event *livekit.WebhookEvent) *http.Request {
	t.Helper()
	body := webhookBody(t, event)
	return webhookRequest(body, webhookToken(t, webhookSecret, body))
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

// countingBody — тело заданной длины, не занимающее памяти; считает,
// сколько из него прочитали.
type countingBody struct {
	left, read int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > b.left {
		n = b.left
	}
	for i := range p[:n] {
		p[i] = 'a'
	}
	b.left -= n
	b.read += n
	return int(n), nil
}

// Подпись проверяется по прочитанному телу, поэтому тело ограничено
// до проверки: 64 MiB без подписи дочитываются не дальше потолка в 1 MiB.
func TestWebhookBodyLimit(t *testing.T) {
	a := &API{cfg: Config{LiveKitURL: "wss://rooms.example", LiveKitAPIKey: webhookKey, LiveKitAPISecret: webhookSecret}}
	body := &countingBody{left: 64 << 20}
	r := httptest.NewRequest(http.MethodPost, "/webhooks/livekit", body)
	r.Header.Set("Authorization", "forged")
	w := httptest.NewRecorder()
	a.Router().ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("ответ %d, хотели 413", w.Code)
	}
	if body.read > maxWebhookBody+1 {
		t.Errorf("прочитано %d байт при потолке %d", body.read, maxWebhookBody)
	}
}

var egressSeq atomic.Int64

// egressFake — серверный API LiveKit: на StartRoomCompositeEgress отвечает
// тем, что вернёт respond, и запоминает, куда просили писать.
type egressFake struct {
	url   string
	calls atomic.Int32
	mu    sync.Mutex
	paths []string
}

func fakeEgress(t *testing.T, respond func(w http.ResponseWriter)) *egressFake {
	t.Helper()
	f := &egressFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/twirp/livekit.Egress/StartRoomCompositeEgress" {
			http.NotFound(w, r)
			return
		}
		f.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		var req livekit.RoomCompositeEgressRequest
		if err := protojson.Unmarshal(body, &req); err == nil && len(req.FileOutputs) == 1 {
			f.mu.Lock()
			f.paths = append(f.paths, req.FileOutputs[0].Filepath)
			f.mu.Unlock()
		}
		respond(w)
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func (f *egressFake) filepaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

// egressStarted — Egress запущен; id у каждого запуска свой, как у LiveKit.
func egressStarted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"egressId":"EG_%d"}`, egressSeq.Add(1))
}

// recordingStand — api с настроенными LiveKit (адрес — fake) и хранилищем
// поверх изолированной схемы securityDB, плюс урок группы в статусе
// scheduled, идущий сейчас (окно комнаты открыто).
type recordingStand struct {
	h        http.Handler
	conn     *pgx.Conn
	st       *store.Store
	egress   *egressFake
	lessonID int64
	teacher  *http.Cookie
}

func newRecordingStand(t *testing.T, respond func(w http.ResponseWriter)) *recordingStand {
	t.Helper()
	base, conn := securityDB(t)
	ctx := context.Background()
	teacherID, teacher := securityUser(t, base, conn, store.RoleTeacher, "rec-teacher")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('rec','B1') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	lesson, err := base.store.CreateLesson(ctx, &groupID, "", teacherID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	egress := fakeEgress(t, respond)
	a, err := New(base.store, Config{
		LiveKitURL: egress.url, LiveKitAPIKey: webhookKey, LiveKitAPISecret: webhookSecret,
		Storage: storage.Config{
			Bucket: "lingua-class", Endpoint: "https://acc.r2.cloudflarestorage.com",
			AccessKey: "key", SecretKey: "secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &recordingStand{h: a.Router(), conn: conn, st: base.store, egress: egress, lessonID: lesson.ID, teacher: teacher}
}

func (s *recordingStand) deliver(t *testing.T, event *livekit.WebhookEvent) int {
	t.Helper()
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, signedWebhook(t, event))
	return w.Code
}

func (s *recordingStand) room(t *testing.T, event, sid string) int {
	t.Helper()
	return s.deliver(t, &livekit.WebhookEvent{Event: event, Room: &livekit.Room{Name: roomName(s.lessonID), Sid: sid}})
}

func (s *recordingStand) ended(t *testing.T, info *livekit.EgressInfo) int {
	t.Helper()
	if info.RoomName == "" {
		info.RoomName = roomName(s.lessonID)
	}
	return s.deliver(t, &livekit.WebhookEvent{Event: eventEgressEnded, EgressInfo: info})
}

// moveLesson ставит урок на startsIn от текущего момента, длиной в час.
func moveLesson(t *testing.T, conn *pgx.Conn, lessonID int64, startsIn time.Duration) {
	t.Helper()
	if _, err := conn.Exec(context.Background(),
		`UPDATE lessons SET starts_at = now() + $2::bigint * interval '1 second',
		                    ends_at = now() + $2::bigint * interval '1 second' + interval '1 hour'
		 WHERE id = $1`, lessonID, int64(startsIn/time.Second)); err != nil {
		t.Fatal(err)
	}
}

type segment struct {
	egressID, status, key, cause string
}

func segments(t *testing.T, conn *pgx.Conn, lessonID int64) []segment {
	t.Helper()
	rows, err := conn.Query(context.Background(),
		`SELECT egress_id, status, COALESCE(s3_key, ''), COALESCE(error, '')
		 FROM lesson_recordings WHERE lesson_id = $1 ORDER BY id`, lessonID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (segment, error) {
		var s segment
		return s, r.Scan(&s.egressID, &s.status, &s.key, &s.cause)
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// lessonState — всё, что вебхук может изменить у урока, одной строкой:
// статус, выбранная запись, сегменты и джобы.
func lessonState(t *testing.T, conn *pgx.Conn, lessonID int64) string {
	t.Helper()
	var status string
	var recording *string
	var queued int
	if err := conn.QueryRow(context.Background(),
		`SELECT status, recording_s3_key, (SELECT count(*) FROM jobs) FROM lessons WHERE id = $1`,
		lessonID).Scan(&status, &recording, &queued); err != nil {
		t.Fatal(err)
	}
	key := "<nil>"
	if recording != nil {
		key = *recording
	}
	return fmt.Sprintf("%s rec=%s jobs=%d segments=%v", status, key, queued, segments(t, conn, lessonID))
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
	s := newRecordingStand(t, func(w http.ResponseWriter) {
		if down.Load() {
			http.Error(w, `{"code":"unavailable","msg":"egress down"}`, http.StatusServiceUnavailable)
			return
		}
		egressStarted(w)
	})
	step := func(name, event string, wantCode int, wantCalls int32, wantStatus string) {
		t.Helper()
		if code := s.room(t, event, ""); code != wantCode {
			t.Errorf("%s: ответ %d, хотели %d", name, code, wantCode)
		}
		if got := s.egress.calls.Load(); got != wantCalls {
			t.Errorf("%s: запусков Egress %d, хотели %d", name, got, wantCalls)
		}
		if got := lessonStatus(t, s.conn, s.lessonID); got != wantStatus {
			t.Errorf("%s: статус %q, хотели %q", name, got, wantStatus)
		}
	}

	step("room_started, Egress упал", eventRoomStarted, http.StatusServiceUnavailable, 1, "scheduled")
	step("повтор room_started, Egress всё ещё лежит", eventRoomStarted, http.StatusServiceUnavailable, 2, "scheduled")
	if segs := segments(t, s.conn, s.lessonID); len(segs) != 0 {
		t.Fatalf("сорванные запуски завели сегменты: %v", segs)
	}
	down.Store(false)
	step("participant_joined после сбоя", eventParticipantJoined, http.StatusOK, 3, "live")
	step("повтор room_started при идущей записи", eventRoomStarted, http.StatusOK, 3, "live")
	step("ещё participant_joined", eventParticipantJoined, http.StatusOK, 3, "live")

	segs := segments(t, s.conn, s.lessonID)
	paths := s.egress.filepaths()
	if len(segs) != 1 || segs[0].status != "active" || len(paths) != 3 || segs[0].key != paths[2] {
		t.Fatalf("сегменты %v, Egress просили писать в %v", segs, paths)
	}
}

// Две одновременные доставки (room_started и participant_joined первого
// участника приходят почти вместе) запускают один Egress, и вторая
// не ждёт, пока первая договорится с LiveKit.
func TestRecordingStartsOnce(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var n atomic.Int32
	s := newRecordingStand(t, func(w http.ResponseWriter) {
		if n.Add(1) == 1 {
			close(entered)
			<-release
		}
		egressStarted(w)
	})
	deliver := func(event string) <-chan int {
		r := signedWebhook(t, &livekit.WebhookEvent{Event: event, Room: &livekit.Room{Name: roomName(s.lessonID)}})
		done := make(chan int, 1)
		go func() {
			w := httptest.NewRecorder()
			s.h.ServeHTTP(w, r)
			done <- w.Code
		}()
		return done
	}

	first := deliver(eventRoomStarted)
	select {
	case <-entered:
	case code := <-first:
		t.Fatalf("первая доставка не дошла до Egress: ответ %d", code)
	case <-time.After(5 * time.Second):
		t.Fatal("первая доставка не дошла до Egress и не ответила за 5 с")
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
	if got := s.egress.calls.Load(); got != 1 {
		t.Errorf("запусков Egress %d, хотели 1", got)
	}
	if got := lessonStatus(t, s.conn, s.lessonID); got != "live" {
		t.Errorf("статус %q, хотели live", got)
	}
}

// Вход гостя не ждёт запуска записи (ADR-031): запуск держит урок
// FOR NO KEY UPDATE на время вызова LiveKit (до 15 с), вход читает его
// FOR KEY SHARE, и режимы совместимы. С прежней парой FOR UPDATE / FOR SHARE
// гость стоял бы за вызовом LiveKit и упирался в statement_timeout.
func TestGuestJoinDoesNotWaitForEgressStart(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	s := newRecordingStand(t, func(w http.ResponseWriter) {
		enterOnce.Do(func() { close(entered) })
		<-release
		egressStarted(w)
	})
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	ctx := context.Background()
	// Урок стенда — открытое событие с гостевой ссылкой
	if _, err := s.conn.Exec(ctx, `UPDATE lessons SET group_id = NULL, title = 'Открытый урок' WHERE id = $1`, s.lessonID); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetGuestToken(ctx, s.lessonID, hashToken("rec-guest-link")); err != nil {
		t.Fatal(err)
	}

	r := signedWebhook(t, &livekit.WebhookEvent{Event: eventRoomStarted, Room: &livekit.Room{Name: roomName(s.lessonID), Sid: "RM_1"}})
	started := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		s.h.ServeHTTP(w, r)
		started <- w.Code
	}()
	select {
	case <-entered:
	case code := <-started:
		t.Fatalf("запуск не дошёл до Egress: ответ %d", code)
	case <-time.After(5 * time.Second):
		t.Fatal("запуск не дошёл до Egress за 5 с")
	}

	// Запуск стоит в вызове LiveKit с захваченным уроком — гость входит сразу
	joinCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, lessonID, err := s.st.JoinAsGuest(joinCtx, hashToken("rec-guest-link"), "Гость", "guest+rec@guest.invalid",
		hashToken("rec-guest-session"), time.Now().Add(time.Hour))
	if err != nil || lessonID != s.lessonID {
		t.Fatalf("вход гостя во время запуска записи: урок %d, %v", lessonID, err)
	}
	select {
	case code := <-started:
		t.Fatalf("запуск закончился раньше входа (%d) — блокировку не проверили", code)
	default:
	}

	unblock()
	select {
	case code := <-started:
		if code != http.StatusOK {
			t.Errorf("запуск записи: ответ %d, хотели 200", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("запуск записи не закончился после ответа Egress")
	}
	if got := lessonStatus(t, s.conn, s.lessonID); got != "live" {
		t.Errorf("статус %q, хотели live", got)
	}
}

// Комната опустела посреди урока (преподаватель вышел, у всех упала сеть):
// egress_ended закрывает сегмент, а не урок. Урок остаётся live, в комнату
// пускают, новая сессия комнаты пишется в свой файл, и ничего не
// перезаписывается. После окна уборка выбирает самую большую запись
// и отдаёт её в обработку (ADR-031).
func TestEgressEndedMidLessonKeepsLessonLive(t *testing.T) {
	s := newRecordingStand(t, egressStarted)
	ctx := context.Background()

	if code := s.room(t, eventRoomStarted, "RM_first"); code != http.StatusOK {
		t.Fatalf("room_started: %d", code)
	}
	first := segments(t, s.conn, s.lessonID)
	if len(first) != 1 || first[0].status != "active" || !segmentKey(s.lessonID, "mp4").MatchString(first[0].key) {
		t.Fatalf("после room_started сегменты %v", first)
	}
	if code := s.ended(t, &livekit.EgressInfo{
		EgressId: first[0].egressID, RoomId: "RM_first", Status: livekit.EgressStatus_EGRESS_COMPLETE,
		FileResults: []*livekit.FileInfo{{Filename: first[0].key, Size: 2 << 20}},
	}); code != http.StatusOK {
		t.Fatalf("egress_ended: %d", code)
	}
	if state := lessonState(t, s.conn, s.lessonID); !strings.HasPrefix(state, "live rec=<nil> jobs=0 ") {
		t.Fatalf("после egress_ended посреди урока: %s", state)
	}
	expectStatus(t, securityRequest(s.h, "GET", fmt.Sprintf("/lessons/%d/room-token", s.lessonID), "", s.teacher), http.StatusOK)

	// Комната открылась снова: новая сессия, новый сегмент; ещё один вошедший
	// той же сессии второго Egress не запускает. Следующая сессия пишется
	// своим сегментом, даже если egress_ended прошлой ещё в пути
	if code := s.room(t, eventRoomStarted, "RM_second"); code != http.StatusOK {
		t.Fatalf("второй room_started: %d", code)
	}
	if code := s.room(t, eventParticipantJoined, "RM_second"); code != http.StatusOK {
		t.Fatalf("participant_joined: %d", code)
	}
	if got := s.egress.calls.Load(); got != 2 {
		t.Fatalf("запусков Egress %d, хотели 2", got)
	}
	if code := s.room(t, eventRoomStarted, "RM_third"); code != http.StatusOK {
		t.Fatalf("третий room_started: %d", code)
	}
	if got := s.egress.calls.Load(); got != 3 {
		t.Fatalf("запусков Egress %d, хотели 3", got)
	}
	segs := segments(t, s.conn, s.lessonID)
	if len(segs) != 3 || segs[0].status != "complete" || segs[1].status != "active" || segs[2].status != "active" ||
		segs[0].key == segs[1].key || segs[1].key == segs[2].key {
		t.Fatalf("сегменты %v", segs)
	}
	if paths := s.egress.filepaths(); len(paths) != 3 || paths[0] != segs[0].key || paths[1] != segs[1].key ||
		paths[2] != segs[2].key {
		t.Fatalf("Egress писал в %v, сегменты %v", paths, segs)
	}
	for i, size := range map[int]int64{1: 1 << 20, 2: 512 << 10} {
		if code := s.ended(t, &livekit.EgressInfo{
			EgressId: segs[i].egressID, Status: livekit.EgressStatus_EGRESS_COMPLETE,
			FileResults: []*livekit.FileInfo{{Filename: segs[i].key, Size: size}},
		}); code != http.StatusOK {
			t.Fatalf("egress_ended сегмента %d: %d", i, code)
		}
	}
	if got := lessonStatus(t, s.conn, s.lessonID); got != "live" {
		t.Fatalf("урок %s до конца окна", got)
	}

	// Окно закрылось — уборка по расписанию завершает урок: запись —
	// больший первый сегмент, джоба recording_ready поставлена
	moveLesson(t, s.conn, s.lessonID, -2*time.Hour)
	runSweep(t, s.st)
	want := fmt.Sprintf("processing rec=%s jobs=1 ", segs[0].key)
	if state := lessonState(t, s.conn, s.lessonID); !strings.HasPrefix(state, want) {
		t.Fatalf("после уборки: %s, хотели %s…", state, want)
	}
	var queued int
	if err := s.conn.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND payload = $2`,
		store.JobRecordingReady, map[string]int64{"lesson_id": s.lessonID}).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("джоб recording_ready: %d (%v)", queued, err)
	}
	expectStatus(t, securityRequest(s.h, "GET", fmt.Sprintf("/lessons/%d/room-token", s.lessonID), "", s.teacher), http.StatusConflict)
}

// runSweep — тик расписания worker'а и выполнение поставленной уборки,
// без ожидания реального периода.
func runSweep(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	runner := jobs.NewRunner(st.Pool(), jobs.Config{WorkerID: "sweep-worker"})
	scheduler := jobs.NewScheduler(st.Pool())
	worker.RegisterRecordingSweep(runner, scheduler, st)
	// Слот периода мог быть занят прошлым вызовом: «выполнить сейчас» —
	// удалить строку kind (ADR-021)
	if _, err := st.Pool().Exec(ctx, `DELETE FROM job_schedules WHERE kind = $1`, store.JobRecordingSweep); err != nil {
		t.Fatal(err)
	}
	if kinds, err := scheduler.Tick(ctx); err != nil || len(kinds) != 1 || kinds[0] != store.JobRecordingSweep {
		t.Fatalf("Tick = %v, %v", kinds, err)
	}
	// Runner знает только kind уборки: recording_ready в очереди он не берёт
	if worked, err := runner.RunOnce(ctx); err != nil || !worked {
		t.Fatalf("RunOnce = %v, %v", worked, err)
	}
	var left int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1`, store.JobRecordingSweep).Scan(&left); err != nil || left != 0 {
		t.Fatalf("уборка не выполнилась: осталось %d (%v)", left, err)
	}
}

// Сегмент, который не удался (EGRESS_FAILED даже с файлами, ABORTED,
// COMPLETE без файлов), записывается failed: вебхук отвечает 200 — повтор
// ничего не даст, — урок посреди окна остаётся live, выбранной записи
// и джоб нет. После окна урок не остаётся live навсегда: уборка переводит
// его в done без записи, потерянный сегмент (egress_ended так и не пришёл)
// — тоже. egress_ended уже после окна завершает урок сразу.
func TestEgressFailureDoesNotLeaveLessonLive(t *testing.T) {
	s := newRecordingStand(t, egressStarted)
	if code := s.room(t, eventRoomStarted, "RM_1"); code != http.StatusOK {
		t.Fatalf("room_started: %d", code)
	}
	started := segments(t, s.conn, s.lessonID)[0]
	file := []*livekit.FileInfo{{Filename: started.key, Size: 1 << 20}}
	for _, c := range []struct {
		name string
		info *livekit.EgressInfo
	}{
		{"запись не удалась, файл есть", &livekit.EgressInfo{EgressId: started.egressID,
			Status: livekit.EgressStatus_EGRESS_FAILED, Error: "egress crashed", FileResults: file}},
		{"запуск прерван", &livekit.EgressInfo{EgressId: "EG_aborted", Status: livekit.EgressStatus_EGRESS_ABORTED}},
		{"файлов нет", &livekit.EgressInfo{EgressId: "EG_empty", Status: livekit.EgressStatus_EGRESS_COMPLETE}},
	} {
		if code := s.ended(t, c.info); code != http.StatusOK {
			t.Errorf("%s: ответ %d, хотели 200", c.name, code)
		}
		if state := lessonState(t, s.conn, s.lessonID); !strings.HasPrefix(state, "live rec=<nil> jobs=0 ") {
			t.Errorf("%s: %s — хотели live без записи и джоб", c.name, state)
		}
	}
	segs := segments(t, s.conn, s.lessonID)
	if len(segs) != 3 {
		t.Fatalf("сегменты %v", segs)
	}
	for _, seg := range segs {
		if seg.status != "failed" || seg.cause == "" {
			t.Errorf("сегмент %+v, хотели failed с причиной", seg)
		}
	}
	if segs[0].key != started.key || !strings.Contains(segs[0].cause, "egress crashed") {
		t.Errorf("сбой с файлом: %+v", segs[0])
	}

	// Ни одна запись не удалась, окно закрылось — уборка ставит done
	moveLesson(t, s.conn, s.lessonID, -2*time.Hour)
	runSweep(t, s.st)
	if state := lessonState(t, s.conn, s.lessonID); !strings.HasPrefix(state, "done rec=<nil> jobs=0 ") {
		t.Fatalf("после уборки: %s", state)
	}

	// egress_ended так и не пришёл: сегмент потерян через 3 ч после конца
	// урока, и урок тоже не ждёт вечно
	lost := newRecordingStand(t, egressStarted)
	if code := lost.room(t, eventRoomStarted, "RM_lost"); code != http.StatusOK {
		t.Fatalf("room_started: %d", code)
	}
	moveLesson(t, lost.conn, lost.lessonID, -2*time.Hour)
	runSweep(t, lost.st)
	if got := lessonStatus(t, lost.conn, lost.lessonID); got != "live" {
		t.Fatalf("урок %s, пока сегмент ещё может закончиться", got)
	}
	moveLesson(t, lost.conn, lost.lessonID, -5*time.Hour)
	runSweep(t, lost.st)
	if state := lessonState(t, lost.conn, lost.lessonID); !strings.HasPrefix(state, "done rec=<nil> jobs=0 ") ||
		!strings.Contains(state, "egress_ended не пришёл") {
		t.Fatalf("потерянный сегмент: %s", state)
	}

	// Сбой, о котором узнали уже после окна, завершает урок сразу
	late := newRecordingStand(t, egressStarted)
	if code := late.room(t, eventRoomStarted, "RM_late"); code != http.StatusOK {
		t.Fatalf("room_started: %d", code)
	}
	seg := segments(t, late.conn, late.lessonID)[0]
	moveLesson(t, late.conn, late.lessonID, -2*time.Hour)
	if code := late.ended(t, &livekit.EgressInfo{EgressId: seg.egressID, Status: livekit.EgressStatus_EGRESS_FAILED}); code != http.StatusOK {
		t.Fatalf("egress_ended: %d", code)
	}
	if got := lessonStatus(t, late.conn, late.lessonID); got != "done" {
		t.Fatalf("урок %s после сбоя за окном", got)
	}
}

// egress_ended, повтор которого ничего не даст, отвечает 200 и урок не
// трогает: нет EgressInfo, чужая комната, нет id Egress, урока нет.
func TestEgressEndedIgnoredAnswers200(t *testing.T) {
	s := newRecordingStand(t, egressStarted)
	if code := s.room(t, eventRoomStarted, ""); code != http.StatusOK {
		t.Fatalf("room_started: %d", code)
	}
	before := lessonState(t, s.conn, s.lessonID)
	files := []*livekit.FileInfo{{Filename: fmt.Sprintf("recordings/%d/x.mp4", s.lessonID), Size: 1}}
	for _, c := range []struct {
		name string
		info *livekit.EgressInfo
	}{
		{"без EgressInfo", nil},
		{"чужая комната", &livekit.EgressInfo{EgressId: "EG_other", RoomName: "other-room",
			Status: livekit.EgressStatus_EGRESS_COMPLETE, FileResults: files}},
		{"неканоническое имя комнаты", &livekit.EgressInfo{EgressId: "EG_padded", RoomName: fmt.Sprintf("lesson-0%d", s.lessonID),
			Status: livekit.EgressStatus_EGRESS_COMPLETE, FileResults: files}},
		{"без id Egress", &livekit.EgressInfo{RoomName: roomName(s.lessonID),
			Status: livekit.EgressStatus_EGRESS_COMPLETE, FileResults: files}},
		{"урока нет", &livekit.EgressInfo{EgressId: "EG_missing", RoomName: roomName(s.lessonID + 1000),
			Status: livekit.EgressStatus_EGRESS_COMPLETE, FileResults: files}},
	} {
		if code := s.deliver(t, &livekit.WebhookEvent{Event: eventEgressEnded, EgressInfo: c.info}); code != http.StatusOK {
			t.Errorf("%s: ответ %d, хотели 200", c.name, code)
		}
		if state := lessonState(t, s.conn, s.lessonID); state != before {
			t.Errorf("%s: %s, было %s", c.name, state, before)
		}
	}
}

// Вебхук публичный, его единственная аутентификация — подпись. Без неё,
// с мусором, с чужим секретом и с телом, подменённым после подписи, — 401:
// Egress не запускается, урок, сегменты и очередь не меняются. Проверяются
// оба опасных события: в окне participant_joined новой сессии запустил бы
// запись, после окна egress_ended завершил бы урок с чужим файлом.
func TestWebhookForgeriesRejected(t *testing.T) {
	s := newRecordingStand(t, egressStarted)
	if code := s.room(t, eventRoomStarted, "RM_1"); code != http.StatusOK {
		t.Fatalf("room_started: %d", code)
	}
	harmless := webhookBody(t, &livekit.WebhookEvent{Event: eventRoomFinished, Room: &livekit.Room{Name: "other-room"}})
	forgeries := func(phase string, body []byte) {
		t.Helper()
		before := lessonState(t, s.conn, s.lessonID)
		for _, c := range []struct {
			name string
			r    *http.Request
		}{
			{"без подписи", webhookRequest(body, "")},
			{"мусор вместо подписи", webhookRequest(body, "not-a-jwt")},
			{"чужой секрет", webhookRequest(body, webhookToken(t, "another-signing-key-at-least-32-bytes", body))},
			{"тело подменено после подписи", webhookRequest(body, webhookToken(t, webhookSecret, harmless))},
		} {
			w := httptest.NewRecorder()
			s.h.ServeHTTP(w, c.r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s, %s: ответ %d, хотели 401", phase, c.name, w.Code)
			}
			if state := lessonState(t, s.conn, s.lessonID); state != before {
				t.Errorf("%s, %s: состояние %s, было %s", phase, c.name, state, before)
			}
		}
	}

	forgeries("participant_joined в окне", webhookBody(t, &livekit.WebhookEvent{Event: eventParticipantJoined,
		Room: &livekit.Room{Name: roomName(s.lessonID), Sid: "RM_2"}}))
	if got := s.egress.calls.Load(); got != 1 {
		t.Errorf("запусков Egress %d, хотели 1 (только подписанный room_started)", got)
	}

	seg := segments(t, s.conn, s.lessonID)[0]
	moveLesson(t, s.conn, s.lessonID, -2*time.Hour)
	forgeries("egress_ended после окна", webhookBody(t, &livekit.WebhookEvent{Event: eventEgressEnded, EgressInfo: &livekit.EgressInfo{
		EgressId: seg.egressID, RoomName: roomName(s.lessonID), Status: livekit.EgressStatus_EGRESS_COMPLETE,
		FileResults: []*livekit.FileInfo{{Filename: "recordings/999/other-lesson.mp4", Size: 1}},
	}}))
	// Контроль: тело с верной подписью принимается
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, webhookRequest(harmless, webhookToken(t, webhookSecret, harmless)))
	if w.Code != http.StatusOK {
		t.Errorf("подписанный вебхук: %d", w.Code)
	}
}

// jwtExpiry — exp из JWT без проверки подписи (её проверяет rooms_test).
func jwtExpiry(t *testing.T, token string) time.Time {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("не JWT: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return time.Unix(claims.Exp, 0)
}

// Окно комнаты (ADR-031): токен и запуск записи — только с starts_at − 15 мин
// до ends_at + 15 мин. Вход в будущий урок (гость по ссылке за трое суток)
// не переводит его в live и не тратит Egress; в прошедший — тоже.
func TestRoomWindow(t *testing.T) {
	s := newRecordingStand(t, egressStarted)
	token := func(want int) *httptest.ResponseRecorder {
		t.Helper()
		w := securityRequest(s.h, "GET", fmt.Sprintf("/lessons/%d/room-token", s.lessonID), "", s.teacher)
		expectStatus(t, w, want)
		return w
	}
	untouched := func(name string) {
		t.Helper()
		if code := s.room(t, eventRoomStarted, ""); code != http.StatusOK {
			t.Errorf("%s: room_started %d", name, code)
		}
		if code := s.room(t, eventParticipantJoined, ""); code != http.StatusOK {
			t.Errorf("%s: participant_joined %d", name, code)
		}
		if state := lessonState(t, s.conn, s.lessonID); state != "scheduled rec=<nil> jobs=0 segments=[]" ||
			s.egress.calls.Load() != 0 {
			t.Errorf("%s: %s, запусков Egress %d", name, state, s.egress.calls.Load())
		}
	}

	moveLesson(t, s.conn, s.lessonID, 72*time.Hour)
	if w := token(http.StatusConflict); !strings.Contains(w.Body.String(), "room_not_open") {
		t.Errorf("за трое суток: %s", w.Body.String())
	}
	untouched("за трое суток")
	moveLesson(t, s.conn, s.lessonID, 20*time.Minute)
	token(http.StatusConflict)
	untouched("за 20 минут")
	moveLesson(t, s.conn, s.lessonID, -(time.Hour + 20*time.Minute)) // кончился 20 минут назад
	if w := token(http.StatusConflict); !strings.Contains(w.Body.String(), "lesson_finished") {
		t.Errorf("после окна: %s", w.Body.String())
	}
	untouched("после окна")

	// За 10 минут до начала комната открыта: токен живёт до конца окна,
	// вход запускает запись
	moveLesson(t, s.conn, s.lessonID, 10*time.Minute)
	var body roomTokenResponse
	if err := json.Unmarshal(token(http.StatusOK).Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	closes := time.Now().Add(10*time.Minute + time.Hour + store.RoomClosesAfter)
	if exp := jwtExpiry(t, body.Token); exp.After(closes.Add(5*time.Second)) || exp.Before(closes.Add(-time.Minute)) {
		t.Errorf("токен до %s, окно закрывается в %s", exp, closes)
	}
	if code := s.room(t, eventRoomStarted, ""); code != http.StatusOK || lessonStatus(t, s.conn, s.lessonID) != "live" ||
		s.egress.calls.Load() != 1 {
		t.Errorf("вход в окне: ответ %d, статус %s, запусков %d", code, lessonStatus(t, s.conn, s.lessonID), s.egress.calls.Load())
	}
}

// Миграция 0018 обратима: down поверх сегментов и джоб уборки проходит,
// повторный up снова применяется.
func TestMigration0018Reversible(t *testing.T) {
	s := newRecordingStand(t, egressStarted)
	ctx := context.Background()
	if code := s.room(t, eventRoomStarted, "RM_1"); code != http.StatusOK {
		t.Fatalf("room_started: %d", code)
	}
	if _, err := jobs.Enqueue(ctx, s.st.Pool(), store.JobRecordingSweep, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(ctx, `INSERT INTO job_schedules (kind, slot) VALUES ($1, now())`, store.JobRecordingSweep); err != nil {
		t.Fatal(err)
	}
	table := func() bool {
		t.Helper()
		var ok bool
		if err := s.conn.QueryRow(ctx, `SELECT to_regclass('lesson_recordings') IS NOT NULL`).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	testdb.Apply(t, s.conn, "0018_lesson_recordings.down.sql")
	var left int
	if err := s.conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM jobs WHERE kind = $1)
		+ (SELECT count(*) FROM job_schedules WHERE kind = $1)`, store.JobRecordingSweep).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if table() || left != 0 || lessonStatus(t, s.conn, s.lessonID) != "live" {
		t.Fatalf("после down: таблица %v, джоб и расписаний уборки %d", table(), left)
	}
	testdb.Apply(t, s.conn, "0018_lesson_recordings.up.sql")
	if !table() {
		t.Fatal("после повторного up таблицы нет")
	}
}
