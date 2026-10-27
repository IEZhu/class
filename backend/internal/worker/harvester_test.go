package worker

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IEZhu/class/backend/internal/harvester"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/testdb"
)

// harvestProbe — хранилище сборщика, которое только запоминает, что
// джобы до него дошли, и сколько потолка джобы осталось к выбору видео.
type harvestProbe struct {
	mu        sync.Mutex
	polled    bool
	remaining time.Duration
}

func (p *harvestProbe) EnabledSubtitleSources(context.Context) ([]store.SubtitleSource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polled = true
	return nil, nil
}

func (p *harvestProbe) SetSourcePolled(context.Context, int64, string) error { return nil }

func (p *harvestProbe) AddFeedVideos(context.Context, int64, []store.FeedVideo) (int, error) {
	return 0, nil
}

func (p *harvestProbe) NextVideoToHarvest(ctx context.Context) (*store.HarvestVideo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		p.remaining = time.Until(deadline)
	}
	return nil, store.ErrNotFound
}

func (p *harvestProbe) SaveSubtitles(context.Context, int64, string, int, []store.SubtitleSegment) error {
	return nil
}

func (p *harvestProbe) FailHarvest(context.Context, int64, string) error { return nil }

// Проводка роли harvester (ADR-022): расписание ставит обе джобы
// сборщика, runner их выполняет, а потолок попытки — harvester.JobTimeout:
// subtitles_fetch доходит до выбора видео с запасом на него. С потолком
// по умолчанию или без расписания одной из джоб корпус молча перестал бы
// расти.
func TestHarvesterRoleWiring(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	probe := &harvestProbe{}
	runner, scheduler := HarvesterRole(pool, harvester.New(probe, harvester.Config{}))

	kinds, err := scheduler.Tick(ctx)
	slices.Sort(kinds)
	if err != nil || !slices.Equal(kinds, []string{harvester.JobRSSPoll, harvester.JobSubtitlesFetch}) {
		t.Fatalf("расписание поставило %v (%v)", kinds, err)
	}
	for range kinds {
		if worked, err := runner.RunOnce(ctx); err != nil || !worked {
			t.Fatalf("RunOnce = %v, %v", worked, err)
		}
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("джоб осталось %d (%v)", left, err)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if !probe.polled {
		t.Error("rss_poll не дошёл до источников")
	}
	if probe.remaining < harvester.JobTimeout-time.Minute {
		t.Errorf("subtitles_fetch выбрал видео с потолком %s, хотели около %s", probe.remaining, harvester.JobTimeout)
	}
}
