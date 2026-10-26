package harvester

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
)

// DefaultFeedURL — публичный Atom-фид канала или плейлиста YouTube: без
// ключа и квоты Data API, отдаёт 15 последних видео.
const DefaultFeedURL = "https://www.youtube.com/feeds/videos.xml"

// Фид — десятки килобайт; потолок защищает worker от мусорного ответа.
const maxFeedBytes = 4 << 20

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

type atomFeed struct {
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	VideoID   string     `xml:"http://www.youtube.com/xml/schemas/2015 videoId"`
	Title     string     `xml:"title"`
	Author    string     `xml:"author>name"`
	Published string     `xml:"published"`
	Links     []atomLink `xml:"link"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

// feedURL — адрес фида источника: channel_id для канала, playlist_id для
// плейлиста.
func feedURL(base string, src store.SubtitleSource) (string, error) {
	param := map[string]string{"channel": "channel_id", "playlist": "playlist_id"}[src.Kind]
	if param == "" {
		return "", fmt.Errorf("источник %d: вид %q", src.ID, src.Kind)
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(param, src.ExternalID)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// fetchFeed скачивает и разбирает фид источника.
func fetchFeed(ctx context.Context, client *http.Client, base string, src store.SubtitleSource) ([]store.FeedVideo, error) {
	target, err := feedURL(base, src)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("фид %s: HTTP %d", src.ExternalID, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxFeedBytes {
		return nil, fmt.Errorf("фид %s больше %d байт", src.ExternalID, maxFeedBytes)
	}
	return ParseFeed(body)
}

// ParseFeed разбирает Atom-фид YouTube. Shorts пропускаются: в них почти
// нет текста, а каждый вызов yt-dlp — лишний риск блокировки (ADR-022).
// Записи без корректного id видео тоже пропускаются.
func ParseFeed(data []byte) ([]store.FeedVideo, error) {
	var feed atomFeed
	if err := xml.Unmarshal(data, &feed); err != nil {
		return nil, fmt.Errorf("фид: %w", err)
	}
	var videos []store.FeedVideo
	for _, e := range feed.Entries {
		if !videoID.MatchString(e.VideoID) || e.isShort() {
			continue
		}
		v := store.FeedVideo{
			ExternalID: e.VideoID,
			Title:      strings.TrimSpace(e.Title),
			Channel:    strings.TrimSpace(e.Author),
		}
		if v.Title == "" {
			v.Title = e.VideoID
		}
		if v.Channel == "" {
			v.Channel = strings.TrimSpace(feed.Title)
		}
		if t, err := time.Parse(time.RFC3339, e.Published); err == nil {
			v.PublishedAt = &t
		}
		videos = append(videos, v)
	}
	return videos, nil
}

func (e atomEntry) isShort() bool {
	for _, l := range e.Links {
		if l.Rel == "alternate" && strings.Contains(l.Href, "/shorts/") {
			return true
		}
	}
	return false
}
