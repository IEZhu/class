package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

// Значения Config по умолчанию. Восемь попыток с удвоением от минуты
// растягиваются примерно на два часа: столько переживёт сбой внешнего
// сервиса, прежде чем джоба станет мёртвой.
const (
	defaultPollInterval   = time.Second
	defaultMaxAttempts    = 8
	defaultBaseBackoff    = time.Minute
	defaultMaxBackoff     = time.Hour
	defaultHandlerTimeout = 5 * time.Minute
	defaultShutdownGrace  = 20 * time.Second

	// Потолок на запись итога попытки. Итог пишется и после SIGTERM,
	// поэтому контекст у него свой, а не отменённый контекст Run.
	settleTimeout = 5 * time.Second
)

// Config — параметры Runner; нулевые поля получают значения по умолчанию.
type Config struct {
	// WorkerID пишется в locked_by: по нему видно, чья джоба. По умолчанию
	// hostname-pid.
	WorkerID string
	// PollInterval — пауза, когда брать нечего.
	PollInterval time.Duration
	// MaxAttempts — после стольких неудачных попыток джоба мертва.
	MaxAttempts int
	// Пауза перед повтором удваивается с каждой неудачей от BaseBackoff
	// до потолка MaxBackoff.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// HandlerTimeout — потолок одной попытки. Захват старше двух таймаутов
	// считается брошенным: живой worker к этому времени отписался бы.
	HandlerTimeout time.Duration
	// ShutdownGrace — сколько после остановки ждать текущую джобу, прежде
	// чем прервать её и вернуть в очередь.
	ShutdownGrace time.Duration
}

func (c Config) withDefaults() Config {
	if c.WorkerID == "" {
		host, _ := os.Hostname()
		c.WorkerID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = defaultMaxAttempts
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = defaultBaseBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = defaultMaxBackoff
	}
	if c.HandlerTimeout <= 0 {
		c.HandlerTimeout = defaultHandlerTimeout
	}
	if c.ShutdownGrace <= 0 {
		c.ShutdownGrace = defaultShutdownGrace
	}
	return c
}

// backoff — пауза перед следующей попыткой после неудачной попытки attempt.
func (c Config) backoff(attempt int) time.Duration {
	d := c.BaseBackoff
	for i := 1; i < attempt && d < c.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, c.MaxBackoff)
}

// Runner берёт джобы из таблицы и раздаёт их хендлерам по kind.
type Runner struct {
	db       DB
	cfg      Config
	handlers map[string]Handler
}

func NewRunner(db DB, cfg Config) *Runner {
	return &Runner{db: db, cfg: cfg.withDefaults(), handlers: map[string]Handler{}}
}

// Handle регистрирует хендлер до запуска Run. Runner берёт только джобы
// известных ему kind: джоба нового kind, поставленная api раньше выката
// worker'а, дождётся своего хендлера, а не сгорит на «неизвестном kind».
func (r *Runner) Handle(kind string, h Handler) { r.handlers[kind] = h }

// Run берёт джобы, пока не отменён ctx (SIGTERM в cmd/worker). После отмены
// новые джобы не берутся, а текущая доводится до конца либо прерывается
// по истечении ShutdownGrace и возвращается в очередь.
func (r *Runner) Run(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			logf("%v", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(r.cfg.PollInterval):
		}
	}
}

// RunOnce берёт и выполняет одну джобу; false — брать нечего.
func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	job, err := r.claim(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("захват джобы: %w", err)
	}
	r.process(ctx, job)
	return true, nil
}

// claim — одним запросом: SKIP LOCKED не даёт двум worker'ам выбрать одну
// строку, а locked_at держит её за владельцем и после коммита захвата.
// Хендлер работает вне транзакции: долгий HTTP-вызов не держит соединение
// и блокировку строки.
func (r *Runner) claim(ctx context.Context) (Job, error) {
	kinds := make([]string, 0, len(r.handlers))
	for kind := range r.handlers {
		kinds = append(kinds, kind)
	}
	var job Job
	err := r.db.QueryRow(ctx,
		`UPDATE jobs SET locked_at = now(), locked_by = $1, attempts = attempts + 1,
		        -- Подбор брошенной: прошлый владелец не отписался, скорее всего упал
		        last_error = CASE WHEN locked_at IS NULL THEN last_error
		                          ELSE 'захват истёк: ' || locked_by || ' не отписался' END
		 WHERE id = (
		     SELECT id FROM jobs
		     WHERE failed_at IS NULL AND run_at <= now() AND kind = ANY($2)
		       AND (locked_at IS NULL OR locked_at < now() - make_interval(secs => $3))
		     ORDER BY run_at, id
		     LIMIT 1
		     FOR UPDATE SKIP LOCKED)
		 RETURNING id, kind, payload, attempts`,
		r.cfg.WorkerID, kinds, (2*r.cfg.HandlerTimeout).Seconds(),
	).Scan(&job.ID, &job.Kind, &job.Payload, &job.Attempt)
	return job, err
}

// buryJob — джоба мертва: больше не берётся, last_error ($4) объясняет почему.
const buryJob = `UPDATE jobs SET locked_at = NULL, locked_by = NULL, failed_at = now(), last_error = $4`

func (r *Runner) process(ctx context.Context, job Job) {
	if job.Attempt > r.cfg.MaxAttempts {
		// Сюда приходят только брошенные: каждый прошлый захват кончился
		// падением worker'а, и запускать хендлер ещё раз незачем
		r.settle(ctx, job, buryJob, "попытки исчерпаны: прошлые захваты брошены")
		logf("джоба %d (%s) остановлена: попытки кончились падениями worker'а", job.ID, job.Kind)
		return
	}

	interrupted, err := r.call(ctx, job)
	switch {
	case err == nil:
		r.settle(ctx, job, `DELETE FROM jobs`)
		logf("джоба %d (%s) выполнена, попытка %d", job.ID, job.Kind, job.Attempt)
	case interrupted:
		// Прервали мы сами из-за остановки: попытку не списываем
		r.settle(ctx, job, `UPDATE jobs SET locked_at = NULL, locked_by = NULL, attempts = attempts - 1`)
		logf("джоба %d (%s) прервана остановкой worker'а и возвращена в очередь", job.ID, job.Kind)
	case job.Attempt >= r.cfg.MaxAttempts:
		r.settle(ctx, job, buryJob, err.Error())
		logf("джоба %d (%s) остановлена после %d попыток: %v", job.ID, job.Kind, job.Attempt, err)
	default:
		delay := r.cfg.backoff(job.Attempt)
		r.settle(ctx, job,
			`UPDATE jobs SET locked_at = NULL, locked_by = NULL, last_error = $4,
			        run_at = now() + make_interval(secs => $5)`,
			err.Error(), delay.Seconds())
		logf("джоба %d (%s), попытка %d: %v; повтор через %s", job.ID, job.Kind, job.Attempt, err, delay)
	}
}

// call выполняет хендлер с потолком HandlerTimeout. Остановка worker'а
// (отмена ctx) не обрывает его сразу: на завершение есть ShutdownGrace.
// interrupted — хендлер прервали мы, по истечении этого запаса.
func (r *Runner) call(ctx context.Context, job Job) (interrupted bool, err error) {
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.HandlerTimeout)
	defer cancel()
	var cut atomic.Bool
	stop := context.AfterFunc(ctx, func() {
		grace := time.NewTimer(r.cfg.ShutdownGrace)
		defer grace.Stop()
		select {
		case <-grace.C:
			cut.Store(true)
			cancel()
		case <-hctx.Done():
		}
	})
	defer stop()
	// Паника в хендлере — обычная неудачная попытка, а не упавший worker
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
		interrupted = cut.Load()
	}()
	return false, r.handlers[job.Kind](hctx, job)
}

// settle записывает итог попытки: stmt — DELETE или UPDATE … SET без WHERE,
// его параметры начинаются с $4. Условие на locked_by и attempts — токен
// владения: если захват уже истёк и джобу подобрал другой worker,
// запоздалый итог ничего не испортит.
func (r *Runner) settle(ctx context.Context, job Job, stmt string, args ...any) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	tag, err := r.db.Exec(sctx, stmt+` WHERE id = $1 AND locked_by = $2 AND attempts = $3`,
		append([]any{job.ID, r.cfg.WorkerID, job.Attempt}, args...)...)
	switch {
	case err != nil:
		// Джоба останется захваченной и вернётся в очередь по истечении захвата
		logf("джоба %d (%s): итог не записан: %v", job.ID, job.Kind, err)
	case tag.RowsAffected() == 0:
		logf("джоба %d (%s): захват потерян, итог попытки %d не записан", job.ID, job.Kind, job.Attempt)
	}
}

func logf(format string, args ...any) { log.Printf("jobs: "+format, args...) }
