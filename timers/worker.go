package timers

import (
	"context"
	stderrors "errors"
	"sync"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/panicking"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Observability keys the worker adds to the set's.
const (
	panicStackKey  = "timers.panic_stack"
	handlerLateKey = "timers.lateness_ms"
	// timerKeyKey names the one timer a firing span is about, and timerKeysKey
	// the batch a failed write could not retire. Both hold the encoded key — the
	// text in the row's primary key — so a span or a log line leads straight to
	// the row, which is where last_error is.
	timerKeyKey  = "timers.key"
	timerKeysKey = "timers.keys"
)

const (
	// DefaultWorkerPoll is the backstop interval. It is long because it is not
	// how a worker learns a timer is due — NextDue is, and a wakeup is — so
	// shortening it buys nothing but query volume.
	DefaultWorkerPoll = time.Minute

	// DefaultWorkerLease is how long a worker holds a batch before the fleet
	// takes it back. See WorkerConfig.Lease for the arithmetic that has to hold
	// between it, Batch, and Concurrency.
	DefaultWorkerLease = time.Minute

	// DefaultWorkerBatch is how many due timers one pass claims.
	DefaultWorkerBatch = 20

	// DefaultWorkerConcurrency is how many of a batch are fired at once.
	DefaultWorkerConcurrency = 4

	// DefaultWorkerRetryDelay is how long a failed firing is pushed out before
	// it is tried again.
	DefaultWorkerRetryDelay = time.Minute
)

// Handler fires one timer.
//
// It must be idempotent. A lease that lapses while its holder is merely slow
// hands the same firing to somebody else, and both will run — see Claim. Due
// carries what a handler needs to notice that: Attempts is above one on a retry,
// and Reclaimed marks a firing that took over a lapsed lease.
//
// Returning an error holds the timer back by WorkerConfig.RetryDelay and records
// the error on the row. Returning nil retires it forever. A panic is contained,
// counted, and treated as an error — one bad timer must not take the loop down
// with it.
//
// The context is the worker's, so it is cancelled at shutdown. A handler doing
// something that must not be abandoned halfway should not be relying on the
// timer for that anyway: the lease will lapse and the firing will come back,
// which is the recovery mechanism working, not failing.
type Handler[K comparable] func(ctx context.Context, due Due[K]) error

// WorkerConfig configures a Worker.
type WorkerConfig struct {
	// Poll is how long a worker sleeps when it has nothing due and no better
	// information. It is a backstop, not a schedule: a worker sleeps until the
	// nearest outstanding timer is owed, so this only bounds how long a lost
	// wakeup or a failed next-due read can delay a firing.
	Poll time.Duration `env:"POLL" json:"poll,omitempty" yaml:"poll,omitempty"`

	// Lease is how long a claimed batch is held before the fleet may take it
	// back.
	//
	// It has to cover the whole batch, not one handler: a pass claims Batch
	// timers and fires them Concurrency at a time under a single lease, so the
	// bound is (Batch / Concurrency) × the slowest handler. A lease shorter than
	// that means the tail of every batch is reclaimed and fired twice while the
	// first worker is still running it. Validation cannot check this — it does
	// not know how long a handler takes — so it is the one knob worth doing
	// arithmetic on.
	Lease time.Duration `env:"LEASE" json:"lease,omitempty" yaml:"lease,omitempty"`

	// RetryDelay is how long a firing whose handler failed is pushed out before
	// it becomes due again. It is a flat delay rather than a backoff curve
	// because Config.MaxAttempts is what bounds a failing timer; a caller who
	// wants a curve calls Release directly from their own loop.
	RetryDelay time.Duration `env:"RETRY_DELAY" json:"retryDelay,omitempty" yaml:"retryDelay,omitempty"`

	// Batch is how many due timers one pass claims.
	Batch int `env:"BATCH" json:"batch,omitempty" yaml:"batch,omitempty"`

	// Concurrency is how many of a batch are fired at once. One means strictly
	// sequential.
	Concurrency int `env:"CONCURRENCY" json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
}

var _ validation.ValidatableWithContext = (*WorkerConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *WorkerConfig) EnsureDefaults() {
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultWorkerPoll
	}
	if cfg.Lease <= 0 {
		cfg.Lease = DefaultWorkerLease
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = DefaultWorkerRetryDelay
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultWorkerBatch
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultWorkerConcurrency
	}
}

// ValidateWithContext validates a WorkerConfig.
func (cfg *WorkerConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Poll, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.Lease, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.RetryDelay, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.Batch, validation.Required, validation.Min(1)),
		validation.Field(&cfg.Concurrency, validation.Required, validation.Min(1)),
	)
}

// Worker is the claim-fire-complete loop over a timer set.
//
// It is the piece a work queue deliberately leaves to its caller, and a timer
// set supplies because the loop is not the caller's to get wrong: sleeping until
// the next instant, not through it, is the entire behavior being asked for, and
// it is expressible in exactly one way. Callers who want their own loop have
// Claim, Complete, Release, and Wait, which is what this is built from.
//
// A Worker holds no state between passes and owns no goroutine until Run is
// called. Run blocks; stop it by cancelling its context.
type Worker[K comparable] struct {
	timers  *Timers[K]
	handler Handler[K]
	o11y    observability.Observer

	cfg WorkerConfig
}

// NewWorker builds a Worker over an existing set.
//
// It takes a *Timers rather than building one, because a process almost always
// schedules and fires through the same set — the trial-expiry service both
// writes the timer when a trial starts and fires it when it ends — and two
// values over one table would mean two of everything the set carries, including
// its metrics.
func NewWorker[K comparable](
	ctx context.Context,
	cfg *WorkerConfig,
	timers *Timers[K],
	handler Handler[K],
) (*Worker[K], error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}
	if timers == nil {
		return nil, ErrNilTimers
	}
	if handler == nil {
		return nil, ErrNilHandler
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating timer worker config")
	}

	return &Worker[K]{
		cfg:     *cfg,
		timers:  timers,
		handler: handler,
		o11y:    timers.o11y,
	}, nil
}

// Run fires due timers until ctx is done, then returns ctx.Err wrapped.
//
// Every pass claims a batch, fires it, and retires what succeeded. A pass that
// claims a full batch goes straight round again — there is more owed, and
// sleeping between full batches would pace a backlog at one batch per poll. A
// pass that claims less than a full batch waits, which is where the sleeping
// until the next instant happens.
//
// Nothing short of a cancelled context stops it. A failed claim is logged and
// slept off: the database being unreachable for a minute is an outage to ride
// out, not a reason for a fleet's timers to stop firing when it comes back.
func (w *Worker[K]) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return platformerrors.Wrap(err, "running the timer worker")
		}

		claimed, err := w.pass(ctx)
		if err != nil {
			// Not returned: see the method comment. The wait below is what keeps
			// a persistent failure from becoming a spin.
			w.timers.o11y.Logger().Error("firing due timers", err)
		}

		// A full batch means more is owed right now. Anything less — including a
		// failed pass, which claimed nothing it can act on — waits.
		if err == nil && claimed >= w.cfg.Batch {
			continue
		}

		if waitErr := w.timers.Wait(ctx, w.cfg.Poll); waitErr != nil {
			return platformerrors.Wrap(waitErr, "running the timer worker")
		}
	}
}

// pass claims one batch and fires it, reporting how many timers it claimed.
func (w *Worker[K]) pass(ctx context.Context) (int, error) {
	due, err := w.timers.Claim(ctx, w.cfg.Batch, w.cfg.Lease)
	if err != nil {
		return 0, err
	}

	if len(due) == 0 {
		return 0, nil
	}

	fired, failed := w.fire(ctx, due)

	// Completion and release run on a context detached from the worker's, so a
	// shutdown that arrives mid-batch still records what was done. Without it a
	// clean deploy would leave every in-flight firing to be reclaimed and run
	// again by whoever comes back up — turning the ordinary case into the
	// duplicate-firing case.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.cfg.Lease)
	defer cancel()

	if len(fired) > 0 {
		if completeErr := w.timers.Complete(writeCtx, fired...); completeErr != nil {
			// Not fatal, and not returned: the handlers ran. The lease lapses and
			// the firings come back, which is a duplicate rather than a loss, and
			// saying so here is more useful than failing a pass that worked.
			w.logKeys(fired).Error("completing fired timers", completeErr)
		}
	}

	for i := range failed {
		if releaseErr := w.timers.
			Release(writeCtx, w.cfg.RetryDelay, failed[i].cause, failed[i].due...); releaseErr != nil {
			w.logKeys(failed[i].due).Error("releasing failed timers", releaseErr)
		}
	}

	return len(due), nil
}

// logKeys returns the worker's logger carrying the encoded keys of a batch.
//
// A write that fails against a batch is the one place the keys are otherwise
// lost entirely: Complete and Release take the whole slice, so a failure says
// only that some timers could not be retired. It is bounded by the configured
// batch size, which is what makes putting the whole list on one line reasonable.
//
// A key that will not encode is skipped rather than rendered some other way. It
// cannot happen for a key that was just decoded out of the table, and a
// placeholder in the list would read as a key nobody can find.
func (w *Worker[K]) logKeys(due []Due[K]) logging.Logger {
	encoded := make([]string, 0, len(due))

	for i := range due {
		if key, err := encodeKey(w.timers.codec, due[i].Key); err == nil {
			encoded = append(encoded, key)
		}
	}

	return w.timers.o11y.Logger().WithValue(timerKeysKey, encoded)
}

// failureGroup is the firings one distinct handler error accounted for, and one
// error to record for all of them.
type failureGroup[K comparable] struct {
	cause error
	due   []Due[K]
}

// fire runs the handler over a claimed batch, Concurrency at a time, and splits
// the result into what succeeded and what did not.
//
// Failures are grouped rather than released one at a time, because the common
// failure is one dependency being down and taking the whole batch with it: a
// single release per distinct error turns a batch of twenty identical timeouts
// into one statement.
//
// They are grouped by rendered message rather than by error value. An error is
// not reliably usable as a map key — a dynamic type with a slice field is not
// comparable and would panic on insert — and two separately wrapped copies of
// one timeout are distinct values but the same failure, which is the thing worth
// merging.
func (w *Worker[K]) fire(ctx context.Context, due []Due[K]) (fired []Due[K], failed []failureGroup[K]) {
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	byMessage := make(map[string]int, len(due))
	slots := make(chan struct{}, w.cfg.Concurrency)

	for i := range due {
		// Cancellation stops handing out new firings but never abandons one
		// already running: the loop below waits for them, and the writes that
		// record them run on a detached context.
		if ctx.Err() != nil {
			break
		}

		slots <- struct{}{}

		wg.Add(1)

		go func(one Due[K]) {
			defer wg.Done()
			defer func() { <-slots }()

			err := w.handle(ctx, one)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				at, seen := byMessage[err.Error()]
				if !seen {
					at = len(failed)
					byMessage[err.Error()] = at
					failed = append(failed, failureGroup[K]{cause: err})
				}

				failed[at].due = append(failed[at].due, one)

				return
			}

			fired = append(fired, one)
		}(due[i])
	}

	wg.Wait()

	return fired, failed
}

// handle fires one timer under its own span, containing whatever the handler
// does to itself.
func (w *Worker[K]) handle(ctx context.Context, due Due[K]) error {
	values := map[string]any{
		attemptKey:     due.Attempts,
		reclaimedKey:   due.Reclaimed,
		handlerLateKey: due.Late.Milliseconds(),
	}

	// The encoded key rather than the Go value, because it is what the row is
	// filed under: a reader who found this span by its error goes to the table
	// next, and anything else makes them guess at the rendering. Without it the
	// span said a timer failed without saying which, and the row's last_error was
	// the only thing that led back.
	if key, err := encodeKey(w.timers.codec, due.Key); err == nil {
		values[timerKeyKey] = key
	}

	ctx, op := w.o11y.Begin(ctx, observability.WithValues(values))
	defer op.End()

	err := panicking.Contain(func() error { return w.handler(ctx, due) })

	// The stack has to reach the span before the PanicError is replaced, since
	// the sentinel that wraps it no longer carries one.
	if pe, ok := stderrors.AsType[*panicking.PanicError](err); ok {
		op.SpanOnly(panicStackKey, string(pe.Stack))

		err = platformerrors.Wrapf(ErrHandlerPanicked, "%v", pe.Value)
	}

	if err != nil {
		return op.Error(err, "firing timer")
	}

	return nil
}
