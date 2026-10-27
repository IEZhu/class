// Package harvester — сборщик субтитров для видео-практики (S5-1, S5-2,
// ADR-022): опрос RSS-фидов каналов из subtitle_sources → videos,
// yt-dlp → WebVTT → subtitle_segments. Видео он не скачивает и ничего
// не транскрибирует. Работает в worker'е с ролью harvester — на VPS или
// на другой машине с тем же DATABASE_URL.
package harvester

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
)

// Джобы сборщика и их расписание (ADR-021). Обе периодические и без
// payload: опрос берёт все включённые источники, сбор — очередь videos.
const (
	JobRSSPoll        = "rss_poll"
	JobSubtitlesFetch = "subtitles_fetch"
	PollEvery         = time.Hour
	FetchEvery        = 5 * time.Minute
	// JobTimeout — потолок одной джобы сборщика (jobs.Config.HandlerTimeout
	// процесса с ролью harvester): пакет subtitles_fetch идёт, пока на
	// следующее видео хватает времени.
	JobTimeout = 15 * time.Minute
)

const (
	// Каждый вызов решает JS-задачи плеера YouTube: запас щедрый, а зависший
	// вызов всё равно оборвётся
	ytdlpTimeout = 5 * time.Minute
	// Сколько нужно на ещё одно видео: пауза темпа плюс вызов yt-dlp
	fetchReserve = maxVideoDelay + ytdlpTimeout
)

// Store — то, что сборщику нужно от Postgres.
type Store interface {
	EnabledSubtitleSources(ctx context.Context) ([]store.SubtitleSource, error)
	SetSourcePolled(ctx context.Context, sourceID int64, pollError string) error
	AddFeedVideos(ctx context.Context, sourceID int64, videos []store.FeedVideo) (int, error)
	NextVideoToHarvest(ctx context.Context) (*store.HarvestVideo, error)
	SaveSubtitles(ctx context.Context, videoID int64, kind string, durationS int, segments []store.SubtitleSegment) error
	FailHarvest(ctx context.Context, videoID int64, reason string) error
}

var _ Store = (*store.Store)(nil)

// Config — зависимости сборщика; нулевые поля получают рабочие значения.
type Config struct {
	// FeedURL — адрес RSS-фидов, по умолчанию YouTube (тесты — httptest)
	FeedURL    string
	HTTPClient *http.Client
	// Fetcher — источник субтитров, по умолчанию yt-dlp из PATH
	Fetcher Fetcher
	Pace    *Pace
}

type Harvester struct {
	store   Store
	feedURL string
	client  *http.Client
	fetcher Fetcher
	pace    *Pace
}

func New(st Store, cfg Config) *Harvester {
	h := &Harvester{store: st, feedURL: cfg.FeedURL, client: cfg.HTTPClient, fetcher: cfg.Fetcher, pace: cfg.Pace}
	if h.feedURL == "" {
		h.feedURL = DefaultFeedURL
	}
	if h.client == nil {
		h.client = &http.Client{Timeout: 30 * time.Second}
	}
	if h.fetcher == nil {
		h.fetcher = YTDLP{}
	}
	if h.pace == nil {
		h.pace = NewPace()
	}
	return h
}

// PollFeeds — джоба rss_poll: новые видео всех включённых источников.
// Сбой одного фида записывается в subtitle_sources.last_error и не мешает
// остальным; следующий опрос — по расписанию. Ошибка джобы — только
// ошибка Postgres.
func (h *Harvester) PollFeeds(ctx context.Context) error {
	sources, err := h.store.EnabledSubtitleSources(ctx)
	if err != nil {
		return err
	}
	for _, src := range sources {
		videos, err := fetchFeed(ctx, h.client, h.feedURL, src)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logf("источник %q (%s): %v", src.Title, src.ExternalID, err)
			if err := h.store.SetSourcePolled(ctx, src.ID, err.Error()); err != nil {
				return err
			}
			continue
		}
		added, err := h.store.AddFeedVideos(ctx, src.ID, videos)
		if err != nil {
			return err
		}
		if err := h.store.SetSourcePolled(ctx, src.ID, ""); err != nil {
			return err
		}
		if added > 0 {
			logf("источник %q: новых видео %d", src.Title, added)
		}
	}
	return nil
}

// FetchSubtitles — джоба subtitles_fetch: пакет видео из очереди в темпе
// Pace. Пакет кончается, когда собирать нечего, на следующее видео не
// хватает времени джобы или YouTube ограничил доступ: тогда до конца паузы
// джобы расписания завершаются сразу, к YouTube не обращаясь.
func (h *Harvester) FetchSubtitles(ctx context.Context) error {
	for first := true; ; first = false {
		if until, paused := h.pace.Blocked(); paused {
			logf("пауза после ограничения YouTube до %s", until.UTC().Format(time.RFC3339))
			return nil
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < fetchReserve {
			if first {
				logf("потолок джобы меньше %s: не хватает даже на одно видео (JobTimeout)", fetchReserve)
			}
			return nil
		}
		v, err := h.store.NextVideoToHarvest(ctx)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := h.pace.Wait(ctx); err != nil {
			return err
		}
		if err := h.harvest(ctx, v); err != nil {
			return err
		}
	}
}

// harvest собирает субтитры одного видео. Ошибка — только та, что ломает
// весь пакет: Postgres, остановка worker'а, отсутствие yt-dlp.
func (h *Harvester) harvest(ctx context.Context, v *store.HarvestVideo) error {
	subs, err := h.fetcher.Fetch(ctx, v.ExternalID)
	switch {
	case err == nil:
	case errors.Is(err, ErrBlocked):
		until := h.pace.Block()
		logf("видео %s: %v; пауза до %s", v.ExternalID, err, until.UTC().Format(time.RFC3339))
		return nil
	case errors.Is(err, ErrNoRunner):
		return err
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		logf("видео %s: %v", v.ExternalID, err)
		return h.store.FailHarvest(ctx, v.ID, err.Error())
	}
	h.pace.Succeeded()

	var segments []store.SubtitleSegment
	if subs.Kind != store.SubtitlesNone {
		if segments, err = ParseVTT(subs.VTT, subs.Kind == store.SubtitlesAuto); err != nil {
			logf("видео %s: %v", v.ExternalID, err)
			return h.store.FailHarvest(ctx, v.ID, err.Error())
		}
		if len(segments) == 0 {
			subs.Kind = store.SubtitlesNone
		}
	}
	if err := h.store.SaveSubtitles(ctx, v.ID, subs.Kind, subs.Duration, segments); err != nil {
		return err
	}
	logf("видео %s: субтитры %s, сегментов %d", v.ExternalID, subs.Kind, len(segments))
	return nil
}

func logf(format string, args ...any) { log.Printf("harvester: "+format, args...) }
