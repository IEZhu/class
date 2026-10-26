package harvester

import (
	"context"
	"testing"
	"time"
)

// fakeClock — время, которое двигает только тест; сон записывается и
// проматывает часы, а не ждёт.
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func fakePace(jitter ...float64) (*Pace, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	i := 0
	p := &Pace{now: clock.Now, sleep: clock.Sleep, jitter: func() float64 {
		if len(jitter) == 0 {
			return 0
		}
		v := jitter[i%len(jitter)]
		i++
		return v
	}}
	return p, clock
}

// Первое обращение не ждёт, следующие — 15–30 с от начала предыдущего;
// время, уже ушедшее на сам вызов, засчитывается.
func TestPaceWaitsJitteredDelayBetweenVideos(t *testing.T) {
	p, clock := fakePace(0, 0, 0.5, 0.999)
	ctx := context.Background()
	for range 4 {
		if err := p.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		clock.now = clock.now.Add(5 * time.Second) // сам вызов yt-dlp
	}
	want := []time.Duration{10 * time.Second, 17500 * time.Millisecond, 24985 * time.Millisecond}
	if len(clock.sleeps) != len(want) {
		t.Fatalf("sleeps %v, want %v", clock.sleeps, want)
	}
	for i, d := range clock.sleeps {
		if d != want[i] {
			t.Errorf("sleep %d = %s, want %s", i, d, want[i])
		}
		if total := d + 5*time.Second; total < minVideoDelay || total > maxVideoDelay {
			t.Errorf("gap %s outside 15–30 s", total)
		}
	}

	// Долгий вызов уже выбрал паузу — ждать нечего
	clock.now = clock.now.Add(time.Minute)
	clock.sleeps = nil
	if err := p.Wait(ctx); err != nil || len(clock.sleeps) != 0 {
		t.Fatalf("waited %v after a long call (%v)", clock.sleeps, err)
	}
}

func TestPaceWaitStopsOnCancel(t *testing.T) {
	p := NewPace()
	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := p.Wait(ctx); err == nil {
		t.Fatal("Wait ignored a cancelled context")
	}
}

// Блокировки подряд удваивают паузу от 30 мин до суток; удачное обращение
// сбрасывает счёт.
func TestPaceBlockBacksOffExponentially(t *testing.T) {
	p, clock := fakePace()
	if _, paused := p.Blocked(); paused {
		t.Fatal("fresh pace is paused")
	}
	var waits []time.Duration
	for range 8 {
		waits = append(waits, p.Block().Sub(clock.now))
	}
	want := []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour,
		8 * time.Hour, 16 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("block %d: pause %s, want %s", i+1, waits[i], want[i])
		}
	}
	until, paused := p.Blocked()
	if !paused || !until.Equal(clock.now.Add(24*time.Hour)) {
		t.Fatalf("Blocked = %s, %v", until, paused)
	}
	clock.now = until
	if _, paused := p.Blocked(); paused {
		t.Fatal("still paused after the pause ended")
	}
	p.Succeeded()
	if got := p.Block().Sub(clock.now); got != baseBlockWait {
		t.Fatalf("after success the pause is %s, want %s", got, baseBlockWait)
	}
}
