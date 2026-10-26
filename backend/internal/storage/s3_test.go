package storage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
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

// Ключ файла материала выбирает api: префикс урока по раскладке
// 05-storage-s3.md и случайное имя, разное у каждого вызова.
func TestNewMaterialKey(t *testing.T) {
	a, err := NewMaterialKey(42, "pdf")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewMaterialKey(42, "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^materials/42/[0-9a-f]{32}\.pdf$`).MatchString(a) {
		t.Errorf("ключ %q не по раскладке materials/{lesson_id}/", a)
	}
	if a == b {
		t.Error("два ключа совпали — имя не случайное")
	}
}

// Тип, длина и Content-Disposition входят в подпись: браузер не загрузит
// по этой ссылке другой тип или другой объём (ADR-024). Host и
// Content-Length ему не отдаём — их выставляет сам браузер.
func TestPresignPutShape(t *testing.T) {
	c, err := New(Config{
		Bucket: "lingua-class", Endpoint: "https://acc.r2.cloudflarestorage.com",
		AccessKey: "AKIAtest", SecretKey: "secret-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	const disposition = `attachment; filename="notes.pdf"`
	put, err := c.PresignPut(context.Background(), "materials/5/abc.pdf", "application/pdf", disposition, 1234, 10*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	u, err := url.Parse(put.URL)
	if err != nil {
		t.Fatal(err)
	}
	if want := "/lingua-class/materials/5/abc.pdf"; u.Path != want {
		t.Errorf("path = %q, хотели %q", u.Path, want)
	}
	q := u.Query()
	if got := q.Get("X-Amz-Expires"); got != "600" {
		t.Errorf("X-Amz-Expires = %q, хотели 600", got)
	}
	signed := strings.Split(q.Get("X-Amz-SignedHeaders"), ";")
	for _, h := range []string{"content-disposition", "content-length", "content-type", "host"} {
		if !slices.Contains(signed, h) {
			t.Errorf("%s не в подписи: %v", h, signed)
		}
	}
	if got := put.Header.Get("Content-Type"); got != "application/pdf" {
		t.Errorf("Content-Type для браузера = %q", got)
	}
	if got := put.Header.Get("Content-Disposition"); got != disposition {
		t.Errorf("Content-Disposition для браузера = %q", got)
	}
	for _, h := range []string{"Host", "Content-Length"} {
		if put.Header.Get(h) != "" {
			t.Errorf("заголовок %s браузер задать не может, а он в списке", h)
		}
	}
	if strings.Contains(put.URL, "secret-value") {
		t.Error("секрет утёк в presigned-ссылку")
	}
}

// Stat и Delete ходят в хранилище — здесь его подменяет httptest-сервер
// с ответами S3 API.
func TestStatAndDelete(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/b/materials/1/present.pdf":
			w.Header().Set("Content-Length", "42")
			w.Header().Set("Content-Type", "application/pdf")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()
	c, err := New(Config{Bucket: "b", Endpoint: srv.URL, AccessKey: "k", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	info, err := c.Stat(ctx, "materials/1/present.pdf")
	if err != nil || info.Size != 42 || info.ContentType != "application/pdf" {
		t.Errorf("Stat = (%+v, %v), хотели 42 байта application/pdf", info, err)
	}
	if _, err := c.Stat(ctx, "materials/1/missing.pdf"); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("Stat отсутствующего = %v, хотели ErrObjectNotFound", err)
	}
	if err := c.Delete(ctx, "materials/1/present.pdf"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "/b/materials/1/present.pdf" {
		t.Errorf("DELETE ушёл на %v", deleted)
	}

	var none *Client
	if _, err := none.Stat(ctx, "k"); err != ErrNotConfigured {
		t.Errorf("Stat на nil-клиенте = %v", err)
	}
	if err := none.Delete(ctx, "k"); err != ErrNotConfigured {
		t.Errorf("Delete на nil-клиенте = %v", err)
	}
}

func TestTranscriptKey(t *testing.T) {
	if got := TranscriptKey(42); got != "transcripts/42.vtt" {
		t.Errorf("TranscriptKey(42) = %q", got)
	}
}
