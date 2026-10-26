package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"

	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/worker"
)

// egress_ended — точка вставки ASR (S1-5, S1-2): путь записи, processing
// и джоба recording_ready атомарно; без ключа AssemblyAI worker сразу
// доводит урок до done и транскрипт не заводит (S2-1). Повторная доставка
// и вебхука, и джобы ничего не ломает.
func TestEgressEndedQueuesRecordingJob(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	teacherID, _ := securityUser(t, a, conn, store.RoleTeacher, "teacher")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('recorded','B1') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	lesson, err := a.store.CreateLesson(ctx, &groupID, "", teacherID, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE lessons SET status = 'live' WHERE id = $1`, lesson.ID); err != nil {
		t.Fatal(err)
	}

	key := storage.RecordingKey(lesson.ID, "mp4")
	event := &livekit.WebhookEvent{Event: eventEgressEnded, EgressInfo: &livekit.EgressInfo{
		RoomName:    roomName(lesson.ID),
		Status:      livekit.EgressStatus_EGRESS_COMPLETE,
		FileResults: []*livekit.FileInfo{{Filename: key, Size: 1 << 20}},
	}}
	h := a.Router()
	deliver := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, signedWebhook(t, event))
		expectStatus(t, w, want)
	}
	expect := func(status, recording string, queued int) {
		t.Helper()
		var gotStatus string
		var gotRecording *string
		if err := conn.QueryRow(ctx, `SELECT status, recording_s3_key FROM lessons WHERE id = $1`,
			lesson.ID).Scan(&gotStatus, &gotRecording); err != nil {
			t.Fatal(err)
		}
		var gotQueued int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND payload = $2`,
			store.JobRecordingReady, map[string]int64{"lesson_id": lesson.ID}).Scan(&gotQueued); err != nil {
			t.Fatal(err)
		}
		if gotStatus != status || (gotRecording == nil) != (recording == "") ||
			(gotRecording != nil && *gotRecording != recording) || gotQueued != queued {
			t.Fatalf("lesson %s/%v with %d jobs, want %s/%q with %d", gotStatus, gotRecording, gotQueued,
				status, recording, queued)
		}
	}

	// Без очереди транзакция откатывается целиком: урок не уходит
	// в processing без джобы, которая его оттуда выведет, а LiveKit
	// получает 503 и повторит доставку
	if _, err := conn.Exec(ctx, `ALTER TABLE jobs RENAME TO jobs_offline`); err != nil {
		t.Fatal(err)
	}
	deliver(http.StatusServiceUnavailable)
	if _, err := conn.Exec(ctx, `ALTER TABLE jobs_offline RENAME TO jobs`); err != nil {
		t.Fatal(err)
	}
	expect("live", "", 0)

	deliver(http.StatusOK) // повтор LiveKit после восстановления
	deliver(http.StatusOK) // и ещё одна доставка того же события
	expect("processing", key, 1)

	runner := jobs.NewRunner(a.store.Pool(), jobs.Config{WorkerID: "test-worker"})
	worker.Register(runner, a.store, worker.ASR{})
	runJob := func() {
		t.Helper()
		worked, err := runner.RunOnce(ctx)
		if err != nil || !worked {
			t.Fatalf("RunOnce = %v, %v", worked, err)
		}
	}
	runJob()
	expect("done", key, 0)

	deliver(http.StatusOK) // запоздалая доставка не возвращает урок в processing
	expect("done", key, 0)

	// at-least-once: та же джоба ещё раз выполняется без ошибки и без перемен
	if _, err := jobs.Enqueue(ctx, a.store.Pool(), store.JobRecordingReady,
		store.RecordingReady{LessonID: lesson.ID}); err != nil {
		t.Fatal(err)
	}
	runJob()
	expect("done", key, 0)
	var left, transcripts int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("jobs left: %d, %v", left, err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM transcripts`).Scan(&transcripts); err != nil || transcripts != 0 {
		t.Fatalf("без ключа AssemblyAI заведено транскриптов: %d, %v", transcripts, err)
	}
}
