package series

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/distributedlock"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultHorizon is how far ahead the worker writes every series: eight
	// weeks, which is two months of a weekly grid a person can look ahead at.
	DefaultHorizon = 8 * 7 * 24 * time.Hour
	// DefaultRefill is how far behind the horizon a series may fall before the
	// worker writes it forward again: a week, so a weekly series is touched once
	// a week rather than on every pass.
	DefaultRefill = 7 * 24 * time.Hour
	// DefaultPollInterval is how often the worker looks for series to write
	// forward. Nothing it writes is due for weeks, so an hour is generous.
	DefaultPollInterval = time.Hour
	// DefaultBatchSize is how many series one read of the due list returns.
	DefaultBatchSize = 100
	// DefaultLockKey is the distributed lock every replica's worker contends
	// for, so one pass runs at a time across the fleet.
	DefaultLockKey = "series_horizon"

	workerName = serviceName + "_horizon"
)

// WorkerConfig configures the horizon worker.
type WorkerConfig struct {
	// LockKey names the distributed lock a pass runs under. Two deployments
	// sharing one lock backend and one database would want the same key; two
	// sharing a lock backend and not a database want different ones.
	LockKey string `env:"LOCK_KEY" json:"lockKey,omitempty" yaml:"lockKey,omitempty"`
	// Horizon is how far ahead of now every series is written. It may not
	// exceed MaxWriteAhead.
	Horizon time.Duration `env:"HORIZON" json:"horizon,omitempty" yaml:"horizon,omitempty"`
	// Refill is how far behind the horizon a series may fall before a pass
	// writes it forward. Zero writes every series forward on every pass. It
	// must be less than Horizon.
	Refill time.Duration `env:"REFILL" json:"refill,omitempty" yaml:"refill,omitempty"`
	// PollInterval is how often a pass runs.
	PollInterval time.Duration `env:"POLL_INTERVAL" json:"pollInterval,omitempty" yaml:"pollInterval,omitempty"`
	// BatchSize is how many due series one read returns. A pass reads batches
	// until one comes back short.
	BatchSize int `env:"BATCH_SIZE" json:"batchSize,omitempty" yaml:"batchSize,omitempty"`
}

var _ validation.ValidatableWithContext = (*WorkerConfig)(nil)

// EnsureDefaults fills unset knobs with their package defaults. Refill is left
// alone, because zero is a meaningful choice for it.
func (cfg *WorkerConfig) EnsureDefaults() {
	if cfg.LockKey == "" {
		cfg.LockKey = DefaultLockKey
	}

	if cfg.Horizon <= 0 {
		cfg.Horizon = DefaultHorizon
	}

	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}

	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
}

// ValidateWithContext validates a WorkerConfig.
func (cfg *WorkerConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.LockKey, validation.Required),
		validation.Field(&cfg.Horizon, validation.Required, validation.Min(time.Minute), validation.Max(MaxWriteAhead)),
		validation.Field(&cfg.Refill, validation.Min(time.Duration(0)), validation.Max(cfg.Horizon-time.Nanosecond)),
		validation.Field(&cfg.PollInterval, validation.Required, validation.Min(time.Second)),
		validation.Field(&cfg.BatchSize, validation.Required, validation.Min(1)),
	)
}

// Worker writes every series forward to the horizon on a timer. It owns a
// goroutine started by Run and stopped by Close.
//
// A pass runs under a distributed lock rather than claiming rows with SKIP
// LOCKED, which is what lets it run on all three dialects: SQLite has no row
// locks to skip, and the work is small enough that one replica doing all of it
// is cheaper than the claim bookkeeping that would let several share it. The
// lock is also not what makes a pass correct. Materialize is idempotent on the
// unique index, so two passes that both ran — a lock that lapsed mid-pass, a
// locker a deployment misconfigured — write one row per slot between them.
//
// It is the component servicing itself, so it reads every tenant's series and
// opens its own transactions, one per series: a series that fails to write
// does not roll back the ones before it.
type Worker struct {
	client database.Client
	store  Store
	locker distributedlock.ScopedLocker
	clock  clock.Clock
	o11y   observability.Observer

	stop chan struct{}
	done chan struct{}

	writtenCounter   metrics.Int64Counter
	failureCounter   metrics.Int64Counter
	contendedCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg WorkerConfig

	stopOnce sync.Once

	// started records that Run was entered, so Close can tell a loop it must
	// wait for from one that was never started.
	started atomic.Bool
}

// WorkerOption configures a Worker.
type WorkerOption func(*Worker)

// WithWorkerClock replaces the clock the horizon is measured from. A nil clock
// leaves the wall clock in place.
func WithWorkerClock(c clock.Clock) WorkerOption {
	return func(w *Worker) {
		if c != nil {
			w.clock = c
		}
	}
}

// WithWorkerLogger attaches a logger. An absent logger logs nowhere.
func WithWorkerLogger(logger logging.Logger) WorkerOption {
	return func(w *Worker) { w.logger = logger }
}

// WithWorkerTracerProvider attaches a tracer provider. An absent provider
// traces nowhere.
func WithWorkerTracerProvider(tracerProvider tracing.Provider) WorkerOption {
	return func(w *Worker) { w.tracerProvider = tracerProvider }
}

// WithWorkerMetricsProvider attaches a metrics provider. An absent provider
// records nothing.
func WithWorkerMetricsProvider(metricsProvider metrics.Provider) WorkerOption {
	return func(w *Worker) { w.metricsProvider = metricsProvider }
}

// WithWorkerPillars attaches a logger, tracer provider, and metrics provider in
// one go. A nil Pillars attaches nothing.
func WithWorkerPillars(p *observability.Pillars) WorkerOption {
	return func(w *Worker) { w.logger, w.tracerProvider, w.metricsProvider = p.Deps() }
}

// NewWorker builds a Worker. It does not start it; call Run, or call RunOnce
// from a scheduler of the deployment's own.
//
// ctx is used to validate the config and is not retained. The client is what
// each series' transaction is opened on and what the due list is read from; the
// locker is required — see ErrNilLocker.
func NewWorker(
	ctx context.Context,
	cfg *WorkerConfig,
	client database.Client,
	store Store,
	locker distributedlock.ScopedLocker,
	opts ...WorkerOption,
) (*Worker, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if store == nil {
		return nil, ErrNilStore
	}

	if locker == nil {
		return nil, ErrNilLocker
	}

	w := &Worker{
		cfg:    *cfg,
		client: client,
		store:  store,
		locker: locker,
		clock:  clock.NewClock(),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}

	w.cfg.EnsureDefaults()

	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}

	if err := w.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating series worker config")
	}

	w.o11y = observability.NewObserver(workerName, w.logger, w.tracerProvider)

	mp := metrics.EnsureMetricsProvider(w.metricsProvider)

	var err error
	if w.writtenCounter, err = mp.NewInt64Counter(workerName + "_occurrences_written"); err != nil {
		return nil, platformerrors.Wrap(err, "creating occurrences written counter")
	}

	if w.failureCounter, err = mp.NewInt64Counter(workerName + "_series_failures"); err != nil {
		return nil, platformerrors.Wrap(err, "creating series failures counter")
	}

	if w.contendedCounter, err = mp.NewInt64Counter(workerName + "_lock_contended"); err != nil {
		return nil, platformerrors.Wrap(err, "creating lock contention counter")
	}

	return w, nil
}

// Run is the worker loop, one pass per PollInterval. Like the other durable
// workers in this module it takes no context: the owner calls Close after the
// server has shut down.
//
// Run returns only after Close.
func (w *Worker) Run() {
	defer close(w.done)

	w.started.Store(true)

	ctx := context.Background()

	ticker := w.clock.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.Chan():
			// RunOnce has already logged and traced a failed pass; there is no
			// caller to hand it to, and the next tick tries again.
			if _, err := w.RunOnce(ctx); err != nil {
				continue
			}
		}
	}
}

// Close stops the worker and waits for the in-flight pass to finish. Safe to
// call more than once, and on a worker that was never started.
func (w *Worker) Close(ctx context.Context) error {
	_, op := w.o11y.Begin(ctx)
	defer op.End()

	w.stopOnce.Do(func() { close(w.stop) })

	if w.started.Load() {
		select {
		case <-w.done:
		case <-ctx.Done():
			return op.Error(ctx.Err(), "waiting for series worker to drain")
		}
	}

	return nil
}

// RunOnce runs one pass: if no other replica holds the lock, every series due
// for writing is written out to the horizon, and the answer is how many
// occurrences the pass wrote. A pass another replica is running is not an
// error; it is zero, and the contention counter says so.
//
// A series that fails to write is logged and counted and does not stop the
// others. The pass stops after the batch it failed in, because a failed series
// is still due and would otherwise be read back into every batch after it.
func (w *Worker) RunOnce(ctx context.Context) (int64, error) {
	ctx, op := w.o11y.Begin(ctx)
	defer op.End()

	var written int64

	acquired, err := w.locker.TryWithLock(ctx, w.cfg.LockKey, func(ctx context.Context) error {
		var passErr error
		written, passErr = w.pass(ctx, op)

		return passErr
	})
	if err != nil {
		return written, op.Error(err, "writing series forward")
	}

	if !acquired {
		w.contendedCounter.Add(ctx, 1)
	}

	op.Set(countKey, written)

	return written, nil
}

// pass is RunOnce's work under the lock.
func (w *Worker) pass(ctx context.Context, op observability.Operation) (int64, error) {
	now := w.clock.Now()
	horizon := now.Add(w.cfg.Horizon)
	due := horizon.Add(-w.cfg.Refill)

	var written int64

	for {
		// The due list is read on the writer: each series this pass advances
		// is committed there, and a replica that had not caught up would hand
		// the same series back.
		batch, err := w.store.DueSeries(ctx, w.client.Writer(), due, w.cfg.BatchSize)
		if err != nil {
			return written, err
		}

		failed := false

		for _, s := range batch {
			var n int64

			txErr := w.client.WithTransaction(ctx, func(tx database.Tx) error {
				var matErr error
				n, matErr = w.store.Materialize(ctx, tx, s.Scope, s.ID, horizon)

				return matErr
			})
			if txErr != nil {
				failed = true

				w.failureCounter.Add(ctx, 1)
				op.Logger().WithValue(seriesIDKey, s.ID).WithValue(scopeKey, s.Scope.String()).Error("writing series forward", txErr)

				continue
			}

			written += n
			w.writtenCounter.Add(ctx, n)
		}

		if failed || len(batch) < w.cfg.BatchSize {
			return written, nil
		}
	}
}
