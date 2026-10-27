package harvester

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// Темп обращений к YouTube (S5-2, ADR-022): одно видео в 15–30 с со
// случайным разбросом; после блокировки — пауза с удвоением от 30 мин
// до суток.
const (
	minVideoDelay = 15 * time.Second
	maxVideoDelay = 30 * time.Second
	baseBlockWait = 30 * time.Minute
	maxBlockWait  = 24 * time.Hour
)

// Pace — темп и backoff одного процесса сборщика. Состояние в памяти,
// а не в Postgres: блокируют IP, а у каждого сборщика (VPS, домашняя
// машина) он свой. После перезапуска процесс один раз проверит, снята ли
// блокировка, и при отказе снова уйдёт в паузу.
type Pace struct {
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
	jitter func() float64 // равномерно в [0, 1)

	mu          sync.Mutex
	last        time.Time // начало последнего обращения
	strikes     int       // блокировки подряд
	pausedUntil time.Time
}

func NewPace() *Pace {
	return &Pace{now: time.Now, sleep: sleepCtx, jitter: rand.Float64}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Wait выдерживает паузу со случайной длиной 15–30 с с начала прошлого
// обращения и отмечает начало нового. Первое обращение процесса не ждёт.
func (p *Pace) Wait(ctx context.Context) error {
	p.mu.Lock()
	delay := minVideoDelay + time.Duration(p.jitter()*float64(maxVideoDelay-minVideoDelay))
	var wait time.Duration
	if !p.last.IsZero() {
		wait = p.last.Add(delay).Sub(p.now())
	}
	p.mu.Unlock()
	if wait > 0 {
		if err := p.sleep(ctx, wait); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.last = p.now()
	p.mu.Unlock()
	return nil
}

// Blocked — сборщик в паузе после блокировки и до какого момента.
func (p *Pace) Blocked() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pausedUntil, p.now().Before(p.pausedUntil)
}

// Block — YouTube отказал (429, проверка на бота): пауза удваивается
// с каждой блокировкой подряд. Возвращает конец паузы.
func (p *Pace) Block() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.strikes++
	wait := baseBlockWait
	for i := 1; i < p.strikes && wait < maxBlockWait; i++ {
		wait *= 2
	}
	p.pausedUntil = p.now().Add(min(wait, maxBlockWait))
	return p.pausedUntil
}

// Succeeded — обращение прошло, счёт блокировок сбрасывается.
func (p *Pace) Succeeded() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.strikes = 0
}
