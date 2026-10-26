package storage

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Раскладка бакета зафиксирована в 05-storage-s3.md и на неё завязаны
// Egress (куда писать) и выдача ссылок (откуда читать).
func TestRecordingKey(t *testing.T) {
	if got := RecordingKey(42, "mp4"); got != "recordings/42/room.mp4" {
		t.Errorf("RecordingKey(42, mp4) = %q", got)
	}
	if got := RecordingKey(1, "ogg"); got != "recordings/1/room.ogg" {
		t.Errorf("RecordingKey(1, ogg) = %q", got)
	}
}

// Без ключей клиент не создаётся, но и не роняет api: стенд поднимается,
// а эндпоинты записи отвечают 503.
func TestNewWithoutCredentials(t *testing.T) {
	c, err := New(Config{Bucket: "b"}) // ключей нет
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c != nil {
		t.Error("клиент создан без ключей")
	}
	if _, err := c.PresignGet(context.Background(), "k", time.Minute); err != ErrNotConfigured {
		t.Errorf("PresignGet на nil-клиенте = %v, хотели ErrNotConfigured", err)
	}
}

// Подпись считается локально, без обращения к хранилищу, — тест офлайновый.
func TestPresignGetShape(t *testing.T) {
	c, err := New(Config{
		Bucket: "lingua-class", Region: "auto",
		Endpoint:  "https://acc.r2.cloudflarestorage.com",
		AccessKey: "AKIAtest", SecretKey: "secret-value",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	raw, err := c.PresignGet(context.Background(), "recordings/5/room.mp4", 30*time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("ссылка не парсится: %v", err)
	}
	if u.Host != "acc.r2.cloudflarestorage.com" {
		t.Errorf("host = %q, хотели endpoint из конфига", u.Host)
	}
	// path-style: бакет в пути, а не в имени хоста — иначе R2 не найдёт объект
	if want := "/lingua-class/recordings/5/room.mp4"; u.Path != want {
		t.Errorf("path = %q, хотели %q", u.Path, want)
	}
	q := u.Query()
	for _, param := range []string{"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Signature", "X-Amz-Expires"} {
		if q.Get(param) == "" {
			t.Errorf("в ссылке нет %s", param)
		}
	}
	if got := q.Get("X-Amz-Expires"); got != "1800" {
		t.Errorf("X-Amz-Expires = %q, хотели 1800", got)
	}
	if !strings.Contains(q.Get("X-Amz-Credential"), "AKIAtest") {
		t.Error("в Credential нет access key")
	}
	// Секрет участвует только в подписи и не должен попасть в ссылку
	if strings.Contains(raw, "secret-value") {
		t.Error("секрет утёк в presigned-ссылку")
	}
}

// Регион по умолчанию auto годится только для R2. Без endpoint это AWS,
// и пустой регион должен ронять старт api, а не выдавать нерабочие ссылки.
func TestNewRegion(t *testing.T) {
	keys := Config{Bucket: "b", AccessKey: "k", SecretKey: "s"}

	if _, err := New(keys); !errors.Is(err, ErrNoRegion) {
		t.Errorf("AWS без региона: err = %v, хотели ErrNoRegion", err)
	}

	aws := keys
	aws.Region = "eu-central-1"
	if c, err := New(aws); err != nil || c == nil {
		t.Errorf("AWS с регионом: (%v, %v), хотели клиент", c, err)
	}

	r2 := keys
	r2.Endpoint = "https://acc.r2.cloudflarestorage.com"
	c, err := New(r2)
	if err != nil || c == nil {
		t.Fatalf("R2 без региона: (%v, %v), хотели клиент", c, err)
	}
	raw, err := c.PresignGet(context.Background(), "k", time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("ссылка не парсится: %v", err)
	}
	if cred := u.Query().Get("X-Amz-Credential"); !strings.Contains(cred, "/auto/s3/") {
		t.Errorf("R2 подписан не регионом auto: %q", cred)
	}
}

func TestTranscriptKey(t *testing.T) {
	if got := TranscriptKey(42); got != "transcripts/42.vtt" {
		t.Errorf("TranscriptKey(42) = %q", got)
	}
}
