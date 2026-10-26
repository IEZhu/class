package harvester

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
)

// Настоящий фид YouTube (урезанный): Shorts и запись без id пропускаются,
// канал — из автора записи, время публикации разобрано.
func TestParseFeedFixture(t *testing.T) {
	videos, err := ParseFeed(readFixture(t, "feed.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, v := range videos {
		ids = append(ids, v.ExternalID)
		if v.Channel != "BBC Learning English" || v.Title == "" || v.PublishedAt == nil {
			t.Errorf("video %+v", v)
		}
	}
	if want := []string{"VFA53-P4yPo", "WXKilGiOQy8", "pjFlVc-EDzc"}; len(ids) != len(want) ||
		ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("ids %v, want %v", ids, want)
	}
	if want := time.Date(2026, 10, 26, 10, 0, 27, 0, time.UTC); !videos[0].PublishedAt.Equal(want) {
		t.Errorf("published %s, want %s", videos[0].PublishedAt, want)
	}
	if want := "Tips for feeling confident in job interviews 🤝 | Improve your listening with Real Easy English"; videos[0].Title != want {
		t.Errorf("title %q", videos[0].Title)
	}
}

func TestParseFeedRejectsGarbage(t *testing.T) {
	if _, err := ParseFeed([]byte("<html>Sorry, something went wrong")); err == nil {
		t.Fatal("garbage feed accepted")
	}
}

func TestFeedURLPerSourceKind(t *testing.T) {
	for kind, want := range map[string]string{
		"channel":  DefaultFeedURL + "?channel_id=UCxyz",
		"playlist": DefaultFeedURL + "?playlist_id=UCxyz",
	} {
		got, err := feedURL(DefaultFeedURL, store.SubtitleSource{Kind: kind, ExternalID: "UCxyz"})
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", kind, got, err, want)
		}
	}
	if _, err := feedURL(DefaultFeedURL, store.SubtitleSource{Kind: "user", ExternalID: "x"}); err == nil {
		t.Error("unknown source kind accepted")
	}
}

func TestFetchFeedReportsHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("channel_id") != "UCgone" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	_, err := fetchFeed(context.Background(), srv.Client(), srv.URL,
		store.SubtitleSource{Kind: "channel", ExternalID: "UCgone"})
	if err == nil || err.Error() != "фид UCgone: HTTP 404" {
		t.Fatalf("error %v", err)
	}
}
