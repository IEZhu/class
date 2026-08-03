package httpapi

import (
	"testing"

	"github.com/livekit/protocol/livekit"

	"github.com/IEZhu/class/backend/internal/storage"
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
