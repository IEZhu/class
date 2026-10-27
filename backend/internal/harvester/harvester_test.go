package harvester

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
)

// memStore — очередь сборщика в памяти; Postgres-версия проверяется
// в httpapi/subtitles_test.go на общей тестовой базе.
type memStore struct {
	queue  []string // external_id в порядке очереди
	saved  map[string]string
	counts map[string]int
	failed map[string]string
}

func newMemStore(ids ...string) *memStore {
	return &memStore{queue: ids, saved: map[string]string{}, counts: map[string]int{}, failed: map[string]string{}}
}

func (m *memStore) EnabledSubtitleSources(context.Context) ([]store.SubtitleSource, error) {
	return nil, nil
}

func (m *memStore) SetSourcePolled(context.Context, int64, string) error { return nil }

func (m *memStore) AddFeedVideos(context.Context, int64, []store.FeedVideo) (int, error) {
	return 0, nil
}

func (m *memStore) NextVideoToHarvest(context.Context) (*store.HarvestVideo, error) {
	if len(m.queue) == 0 {
		return nil, store.ErrNotFound
	}
	return &store.HarvestVideo{ID: int64(len(m.queue)), ExternalID: m.queue[0]}, nil
}

func (m *memStore) SaveSubtitles(_ context.Context, _ int64, kind string, _ int, segments []store.SubtitleSegment) error {
	m.saved[m.queue[0]], m.counts[m.queue[0]] = kind, len(segments)
	m.queue = m.queue[1:]
	return nil
}

func (m *memStore) FailHarvest(_ context.Context, _ int64, reason string) error {
	m.failed[m.queue[0]] = reason
	m.queue = m.queue[1:]
	return nil
}

// fakeFetcher отвечает по id видео и запоминает, к чему обращались.
type fakeFetcher struct {
	results map[string]Subtitles
	errs    map[string]error
	calls   []string
}

func (f *fakeFetcher) Fetch(_ context.Context, id string) (Subtitles, error) {
	f.calls = append(f.calls, id)
	if err := f.errs[id]; err != nil {
		return Subtitles{}, err
	}
	if subs, ok := f.results[id]; ok {
		return subs, nil
	}
	return Subtitles{}, fmt.Errorf("unexpected video %s", id)
}

func testHarvester(t *testing.T, st *memStore, f *fakeFetcher) (*Harvester, *fakeClock) {
	t.Helper()
	pace, clock := fakePace(0.5)
	return New(st, Config{Fetcher: f, Pace: pace}), clock
}

func TestFetchSubtitlesStoresManualAutoAndNone(t *testing.T) {
	st := newMemStore("manualVideo", "autoVideo00", "noSubs00000", "onlyMusic00", "privateVid0")
	f := &fakeFetcher{
		results: map[string]Subtitles{
			"manualVideo": {Kind: store.SubtitlesManual, VTT: readFixture(t, "manual.en.vtt")},
			"autoVideo00": {Kind: store.SubtitlesAuto, VTT: readFixture(t, "auto.en.vtt")},
			"noSubs00000": {Kind: store.SubtitlesNone},
			"onlyMusic00": {Kind: store.SubtitlesAuto, VTT: []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n[Music]\n")},
		},
		errs: map[string]error{"privateVid0": errors.New("yt-dlp: Private video")},
	}
	h, clock := testHarvester(t, st, f)
	if err := h.FetchSubtitles(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"manualVideo": store.SubtitlesManual, "autoVideo00": store.SubtitlesAuto,
		"noSubs00000": store.SubtitlesNone, "onlyMusic00": store.SubtitlesNone,
	} {
		if st.saved[id] != want {
			t.Errorf("%s saved as %q, want %q", id, st.saved[id], want)
		}
	}
	// Ручные — сегмент на реплику, авто — после дедупликации строк
	if st.counts["manualVideo"] != 4 || st.counts["autoVideo00"] != 3 {
		t.Errorf("segments %v", st.counts)
	}
	if st.failed["privateVid0"] != "yt-dlp: Private video" || len(st.queue) != 0 {
		t.Errorf("failed %v, queue %v", st.failed, st.queue)
	}
	// Пять видео — четыре паузы по 22,5 с (jitter 0.5), часы тест не двигал
	if len(clock.sleeps) != 4 {
		t.Fatalf("sleeps %v, want 4 pauses between 5 videos", clock.sleeps)
	}
	for _, d := range clock.sleeps {
		if d != 22500*time.Millisecond {
			t.Errorf("pause %s, want 22.5s", d)
		}
	}
}

// Блокировка обрывает пакет, не записывая попытку видео; до конца паузы
// джобы к YouTube не ходят, после — одно пробное обращение.
func TestFetchSubtitlesBlockedPausesHarvester(t *testing.T) {
	st := newMemStore("firstVideo0", "blockedVid0", "thirdVideo0")
	ok := Subtitles{Kind: store.SubtitlesNone}
	f := &fakeFetcher{
		results: map[string]Subtitles{"firstVideo0": ok, "thirdVideo0": ok},
		errs:    map[string]error{"blockedVid0": fmt.Errorf("%w: HTTP Error 429", ErrBlocked)},
	}
	h, clock := testHarvester(t, st, f)
	ctx := context.Background()
	if err := h.FetchSubtitles(ctx); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.calls) != "[firstVideo0 blockedVid0]" || len(st.failed) != 0 {
		t.Fatalf("calls %v, failed %v", f.calls, st.failed)
	}
	if st.queue[0] != "blockedVid0" {
		t.Fatalf("blocked video left the queue: %v", st.queue)
	}

	clock.now = clock.now.Add(29 * time.Minute)
	if err := h.FetchSubtitles(ctx); err != nil || len(f.calls) != 2 {
		t.Fatalf("called YouTube during the pause: %v, %v", f.calls, err)
	}

	// Пауза прошла, но блокировка не снята: следующая пауза вдвое длиннее
	clock.now = clock.now.Add(2 * time.Minute)
	if err := h.FetchSubtitles(ctx); err != nil || len(f.calls) != 3 {
		t.Fatalf("probe after the pause: %v, %v", f.calls, err)
	}
	until, paused := h.pace.Blocked()
	if !paused || until.Sub(clock.now) != time.Hour {
		t.Fatalf("second pause until %s (%v), want an hour", until, paused)
	}

	clock.now = until
	delete(f.errs, "blockedVid0")
	f.results["blockedVid0"] = ok
	if err := h.FetchSubtitles(ctx); err != nil || len(st.queue) != 0 {
		t.Fatalf("after the block: queue %v, %v", st.queue, err)
	}
}

func TestFetchSubtitlesStopsWithoutYTDLP(t *testing.T) {
	st := newMemStore("firstVideo0")
	f := &fakeFetcher{errs: map[string]error{"firstVideo0": fmt.Errorf("%w: not found", ErrNoRunner)}}
	h, _ := testHarvester(t, st, f)
	if err := h.FetchSubtitles(context.Background()); !errors.Is(err, ErrNoRunner) {
		t.Fatalf("error %v, want ErrNoRunner", err)
	}
	if len(st.failed) != 0 || len(st.queue) != 1 {
		t.Fatalf("environment failure blamed on the video: %v", st.failed)
	}
}

// На следующее видео не хватает времени джобы — пакет кончается сам,
// а не обрывается таймаутом посреди вызова.
func TestFetchSubtitlesLeavesRoomBeforeDeadline(t *testing.T) {
	st := newMemStore("firstVideo0")
	f := &fakeFetcher{results: map[string]Subtitles{"firstVideo0": {Kind: store.SubtitlesNone}}}
	h, _ := testHarvester(t, st, f)
	ctx, cancel := context.WithTimeout(context.Background(), fetchReserve-time.Second)
	defer cancel()
	if err := h.FetchSubtitles(ctx); err != nil || len(f.calls) != 0 {
		t.Fatalf("calls %v, %v", f.calls, err)
	}
}
