package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/storage/storagetest"
)

// Put доходит до хранилища path-style с телом и Content-Type как есть:
// с этим типом браузер подключит VTT к плееру.
func TestPut(t *testing.T) {
	fake := storagetest.New(t)
	c, err := storage.New(fake.Config())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("WEBVTT\n\n00:00:00.000 --> 00:00:01.000\n<v A>hi\n")
	if err := c.Put(context.Background(), "transcripts/7.vtt", "text/vtt; charset=utf-8", body); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := fake.Object("transcripts/7.vtt")
	if !ok || string(got.Body) != string(body) || got.ContentType != "text/vtt; charset=utf-8" {
		t.Fatalf("в хранилище %+v (%v)", got, ok)
	}

	fake.Fail(true)
	if err := c.Put(context.Background(), "transcripts/7.vtt", "text/vtt", body); err == nil {
		t.Fatal("отказ хранилища не вернул ошибку")
	}

	var none *storage.Client
	if err := none.Put(context.Background(), "k", "text/plain", nil); !errors.Is(err, storage.ErrNotConfigured) {
		t.Errorf("Put на nil-клиенте = %v, хотели ErrNotConfigured", err)
	}
}
