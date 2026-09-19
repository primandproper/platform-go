package workqueue

import (
	"context"
	stderrors "errors"
	"fmt"
	"sync"
	"time"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/panicking"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// runnerName scopes the runner's spans, logger and metrics, distinct from the
// queue's so a trace of work being done is not indistinguishable from a trace of
// work being handed out.
const runnerName = serviceName + "_runner"

// Observability keys the runner adds to the queue's.
const (
	panicStackKey = "workqueue.panic_stack"
	// itemKeyKey names the one item a span is about and itemKeysKey the batch a
	// failed write could not report on. Both hold the encoded key — the text in
	// the row's primary key — so a span or a log line leads straight to the row,
	// which is where last_error is.
	itemKeyKey  = "workqueue.key"
	itemKeysKey = "workqueue.keys"
)

const (
	// DefaultRunnerPoll is how long a runner sleeps when it claimed nothing. It
	// is a backstop rather than a schedule: with a wakeup wired, fresh work is
	// claimed in milliseconds and this only bounds how long a lost wake can
	// delay it.
	DefaultRunnerPoll = 5 * time.Second

	// DefaultRunnerLease is how long a claimed batch is held before the fleet
	// may take it back. It is short because it no longer has to cover the work:
	// the runner extends it for as long as a handler is running, and the only
	// thing it has to cover is the gap between two extensions.
	DefaultRunnerLease = time.Minute

	// DefaultRunnerExtendDivisor divides Lease to produce an unset
	// ExtendInterval.
	//
	// The interval is derived rather than defaulted to a constant, because the
	// two are one setting: an interval that does not divide into the lease
	// several times over is an interval whose every missed tick costs a
	// reclaim. Shorten the lease and the heartbeat shortens with it.
	DefaultRunnerExtendDivisor = 3

	// DefaultRunnerRetryDelay is how long an item whose handler failed is held
	// back before it becomes claimable again. It is a flat delay rather than a
	// backoff curve because Config.MaxAttempts is what bounds a failing item; a
	// caller who wants a curve calls Release from a loop of their own.
	DefaultRunnerRetryDelay = 30 * time.Second

	// DefaultRunnerBatch is how many items one pass claims.
	DefaultRunnerBatch = 20

	// DefaultRunnerConcurrency is how many of a batch are handled at once.
	DefaultRunnerConcurrency = 4
)

// Handler does the work one claimed item names.
//
// It must be idempotent. A lease that lapses while its holder is merely slow
// hands the same item to somebody else, and both will run it — the runner's
// lease extension makes that rarer, not impossible, because a worker that is
// paused or partitioned stops extending without knowing it has. Item carries
// what a handler needs to notice: Attempts is above one on a retry, and
// Reclaimed marks an item that took over a lapsed lease.
//
// Returning an error holds the item back by RunnerConfig.RetryDelay and records
// the error on the row. Returning nil retires it. A panic is contained and
// treated as an error — one poisonous item must not take the loop down with it.
//
// The context is the runner's, so it is cancelled at shutdown. A handler that
// returns promptly on cancellation is released and claimed again by whoever
// comes back up; one that runs on keeps its lease extended until it returns,
// which is what makes the shutdown a drain rather than an abandonment.
type Handler[K comparable] func(ctx context.Context, item Item[K]) error

// RunnerConfig configures a Runner.
type RunnerConfig struct {
	// Poll is how long a runner sleeps when it claimed nothing and has no better
	// information. It is a backstop: with a wakeup wired through WithWakeup, a
	// freshly enqueued item is claimed in milliseconds, and this only bounds how
	// long a lost notification can delay one.
	Poll time.Duration `env:"POLL" json:"poll,omitempty" yaml:"poll,omitempty"`

	// Lease is how long a claimed batch is held before the fleet may take it
	// back, and also how far each extension pushes that horizon out.
	//
	// It does not have to cover the work, which is the difference between this
	// loop and one written around Claim by hand: an item whose handler is still
	// running has its lease extended every ExtendInterval. What it has to cover
	// is a missed extension or two — a runner that is briefly unable to reach the
	// database keeps its claims for this long and no longer.
	Lease time.Duration `env:"LEASE" json:"lease,omitempty" yaml:"lease,omitempty"`

	// ExtendInterval is how often the leases on the items currently being
	// handled are pushed out. It defaults to Lease divided by
	// DefaultRunnerExtendDivisor.
	//
	// One statement per interval covers every item in flight, whatever the
	// concurrency: the batch is extended together, because it was claimed
	// together.
	ExtendInterval time.Duration `env:"EXTEND_INTERVAL" json:"extendInterval,omitempty" yaml:"extendInterval,omitempty"`

	// RetryDelay is how long an item whose handler failed is held back before it
	// becomes claimable again.
	RetryDelay time.Duration `env:"RETRY_DELAY" json:"retryDelay,omitempty" yaml:"retryDelay,omitempty"`

	// Batch is how many items one pass claims.
	//
	// It is lowered to the queue's Config.MaxClaimBatch when that is smaller,
	// once at construction rather than by each Claim, and the runner paces
	// itself on the lowered number. Claim would clamp it either way — that is
	// its documented contract — but a loop that kept asking for more than it can
	// receive would judge every pass short and sleep between all of them. See
	// Run.
	Batch int `env:"BATCH" json:"batch,omitempty" yaml:"batch,omitempty"`

	// Concurrency is how many of a batch are handled at once. One means strictly
	// sequential.
	Concurrency int `env:"CONCURRENCY" json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
}

var _ validation.ValidatableWithContext = (*RunnerConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
//
// ExtendInterval is derived from Lease rather than taken from a constant, and
// the order matters: Lease is defaulted first, so a caller who set neither gets
// an interval that divides into the lease it will actually be extending.
func (cfg *RunnerConfig) EnsureDefaults() {
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultRunnerPoll
	}
	if cfg.Lease <= 0 {
		cfg.Lease = DefaultRunnerLease
	}
	if cfg.ExtendInterval <= 0 {
		cfg.ExtendInterval = cfg.Lease / DefaultRunnerExtendDivisor
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = DefaultRunnerRetryDelay
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultRunnerBatch
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultRunnerConcurrency
	}
}

// ValidateWithContext validates a RunnerConfig.
//
// ExtendInterval is required to be at most half of Lease, which is the one
// cross-field rule worth enforcing: the extension is the only thing keeping a
// long handler's claim alive, so an interval that does not fit inside the lease
// twice means every slow item is reclaimed between ticks and run twice — the
// precise failure the extension exists to remove.
func (cfg *RunnerConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Poll, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.Lease, validation.Required, validation.Min(time.Second)),
		validation.Field(&cfg.ExtendInterval, validation.Required,
			validation.Min(100*time.Millisecond), validation.Max(cfg.Lease/2)), //nolint:mnd // half a lease, stated above
		validation.Field(&cfg.RetryDelay, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.Batch, validation.Required, validation.Min(1)),
		validation.Field(&cfg.Concurrency, validation.Required, validation.Min(1)),
	)
}

// Runner is the claim-work-complete loop over a queue.
//
// It is the loop this package used to leave to its caller, and every consumer
// wrote the same one: claim a batch, run it, complete what worked, release what
// did not, go round again until the queue is empty. Nothing in that is a
// consumer's decision except the handler — but two things in it are easy to get
// wrong, and both cost duplicate executions rather than errors. A lease that is
// not extended lapses under a slow handler, and a shutdown that abandons a
// claimed batch leaves every item in it to be reclaimed by whoever comes back
// up. This does both, so a consumer does neither.
//
// A Runner holds no state between passes and owns no goroutine until Run is
// called. Run blocks; stop it by cancelling its context. Callers who want their
// own loop still have Claim, Extend, Complete, Release and Wait, which is what
// this is built from.
type Runner[K comparable] struct {
	queue   *Queue[K]
	handler Handler[K]
	o11y    observability.Observer

	// Every counter and histogram below is recorded with the Queue's own
	// attribute set rather than a second copy of it, because one process
	// commonly runs a runner per logical queue: without the label their
	// measurements collapse into a single number in which a queue whose
	// handlers are failing is invisible beside the ones that are fine.
	handledCounter   metrics.Int64Counter
	failedCounter    metrics.Int64Counter
	panickedCounter  metrics.Int64Counter
	drainedCounter   metrics.Int64Counter
	extendedCounter  metrics.Int64Counter
	lostLeaseCounter metrics.Int64Counter

	durationHist metrics.Float64Histogram

	// batch is how many items a pass actually claims, which is cfg.Batch
	// resolved against the queue's own ceiling rather than cfg.Batch itself.
	//
	// Claim clamps a limit above Config.MaxClaimBatch rather than rejecting it,
	// by that field's documented contract, so a runner asking for more than the
	// queue allows is a supported configuration that quietly never gets what it
	// asked for. Keeping the declared number would make one comparison wrong in
	// a way nothing reports: see Run, where a pass is judged full against this.
	batch int

	cfg RunnerConfig
}

// NewRunner builds a Runner over an existing queue.
//
// The queue is taken rather than built, because a process that drains a queue
// usually enqueues onto it too — the service that schedules a tile render is the
// one that renders it — and two Queue values over one table merge no enqueues
// between them and report two sets of metrics for one queue.
//
// handler is positional because it is the one dependency a runner cannot be
// given any other way: it is the work.
func NewRunner[K comparable](
	ctx context.Context,
	cfg *RunnerConfig,
	queue *Queue[K],
	handler Handler[K],
	opts ...RunnerOption,
) (*Runner[K], error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}
	if queue == nil {
		return nil, ErrNilQueue
	}
	if handler == nil {
		return nil, ErrNilHandler
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating work queue runner config")
	}

	o := newRunnerOptions(opts)

	r := &Runner[K]{
		cfg:     *cfg,
		batch:   cfg.Batch,
		queue:   queue,
		handler: handler,
		o11y:    observability.NewObserver(runnerName, o.logger, o.tracerProvider),
	}

	// The queue's ceiling, applied here rather than discovered a batch at a
	// time. It is not a refusal because Claim's own contract is to clamp rather
	// than reject, and a runner is not the place to overturn that; what it is is
	// the one number this loop reasons about, resolved once so that the loop
	// reasons about what it will actually be handed.
	if queue.cfg.MaxClaimBatch > 0 && r.batch > queue.cfg.MaxClaimBatch {
		r.o11y.Logger().WithValues(map[string]any{
			"workqueue.runner_batch":    r.batch,
			"workqueue.max_claim_batch": queue.cfg.MaxClaimBatch,
		}).Info("work queue runner batch exceeds the queue's claim ceiling and was lowered to it")

		r.batch = queue.cfg.MaxClaimBatch
	}

	if err := r.buildInstruments(metrics.EnsureMetricsProvider(o.metricsProvider)); err != nil {
		return nil, err
	}

	return r, nil
}

// buildInstruments creates every instrument the runner owns, so a failure to
// build one is reported at construction rather than discovered as a silently
// missing series.
func (r *Runner[K]) buildInstruments(mp metrics.Provider) error {
	counters := []struct {
		into *metrics.Int64Counter
		name string
	}{
		{&r.handledCounter, "items_handled"},
		{&r.failedCounter, "items_failed"},
		{&r.panickedCounter, "handler_panics"},
		{&r.drainedCounter, "items_drained"},
		{&r.extendedCounter, "leases_extended"},
		// The one that says the lease is mis-sized. A runner whose extension
		// matched fewer items than it named is a runner whose handlers outran
		// both the lease and the interval keeping it alive — and those items
		// were done twice.
		{&r.lostLeaseCounter, "leases_lost"},
	}
	for _, c := range counters {
		instrument, err := mp.NewInt64Counter(fmt.Sprintf("%s_%s", runnerName, c.name))
		if err != nil {
			return platformerrors.Wrapf(err, "creating %s counter", c.name)
		}

		*c.into = instrument
	}

	hist, err := mp.NewFloat64Histogram(runnerName + "_handler_duration_ms")
	if err != nil {
		return platformerrors.Wrap(err, "creating handler duration histogram")
	}

	r.durationHist = hist

	return nil
}

// Run claims and handles work until ctx is done, then returns ctx.Err wrapped.
//
// A pass that claims a full batch goes straight round again — there is more
// waiting, and sleeping between full batches would pace a backlog at one batch
// per poll. Anything less waits, which is where a wakeup earns its keep.
//
// "Full" is against the batch this runner actually claims, which is
// RunnerConfig.Batch lowered to Config.MaxClaimBatch where the queue's ceiling
// is the smaller of the two. Against the declared number instead, a runner
// configured above that ceiling would never see a full pass, and the fast path
// would be dead code: every batch would be followed by a Poll, pacing a backlog
// at one ceiling per poll with nothing anywhere saying why. The lowering is
// logged once at construction for the same reason.
//
// Nothing short of a cancelled context stops it. A failed claim is logged and
// slept off: the database being unreachable for a minute is an outage to ride
// out, not a reason for a fleet to stop draining a queue when it comes back.
func (r *Runner[K]) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return platformerrors.Wrap(err, "running the work queue runner")
		}

		claimed, err := r.pass(ctx)
		if err != nil {
			// Not returned: see the method comment. The wait below is what keeps
			// a persistent failure from becoming a spin.
			r.o11y.Logger().Error("handling claimed work queue items", err)
		}

		if err == nil && claimed >= r.batch {
			continue
		}

		if waitErr := r.queue.Wait(ctx, r.cfg.Poll); waitErr != nil {
			return platformerrors.Wrap(waitErr, "running the work queue runner")
		}
	}
}

// pass claims one batch and works it, reporting how many items it claimed.
func (r *Runner[K]) pass(ctx context.Context) (int, error) {
	items, err := r.queue.Claim(ctx, r.batch, r.cfg.Lease)
	if err != nil {
		return 0, err
	}

	if len(items) == 0 {
		return 0, nil
	}

	inFlight := &inFlight[K]{}

	// The heartbeat runs on a context detached from the runner's, and with no
	// deadline of its own: what bounds it is the handlers, since it is stopped
	// as soon as they have all returned. A deadline here would be a second,
	// quieter lease — the claims would stop being extended while the work was
	// still running, which is the failure the extension exists to remove.
	// Detaching it is what makes a shutdown a drain: the items a finishing
	// handler holds stay this worker's until it is done with them.
	stopExtending := r.extendWhileRunning(context.WithoutCancel(ctx), inFlight)

	handled, failed, undispatched := r.work(ctx, items, inFlight)

	// Before the outcome writes rather than after them, so the extension cannot
	// push out the lease on an item the completion is retiring in the same
	// breath and count itself fenced for it.
	stopExtending()

	// The outcome writes are detached too, so a shutdown arriving mid-batch
	// still records what became of the work. Without it a clean deploy would
	// leave every in-flight item to be reclaimed and run again by whoever comes
	// back up, turning the ordinary case into the duplicate-execution case.
	//
	// The timeout is a whole lease, which means these writes may well land after
	// the lease they were taken under has expired. That is the clearest reason
	// the fence is the claim's name rather than the lease's liveness: a horizon
	// test would refuse exactly the write this context exists to preserve.
	writeCtx, cancelWrites := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.Lease)
	defer cancelWrites()

	r.record(writeCtx, handled, failed, undispatched)

	return len(items), nil
}

// record writes what a pass decided: a completion for what was handled, a
// hand-back per distinct failure, and a hand-back for whatever a shutdown never
// got to.
func (r *Runner[K]) record(ctx context.Context, handled []Item[K], failed []failureGroup[K], undispatched []Item[K]) {
	if len(handled) > 0 {
		if err := r.queue.Complete(ctx, handled...); err != nil {
			// Not fatal and not returned: the handlers ran. The lease lapses and
			// the items come back, which is a duplicate rather than a loss, and
			// saying which items is more useful than failing a pass that worked.
			r.logKeys(handled).Error("completing handled work queue items", err)
		}

		r.handledCounter.Add(ctx, int64(len(handled)), r.queue.attrs)
	}

	for i := range failed {
		if err := r.queue.Release(ctx, r.cfg.RetryDelay, failed[i].cause, failed[i].items...); err != nil {
			r.logKeys(failed[i].items).Error("releasing failed work queue items", err)
		}

		r.failedCounter.Add(ctx, int64(len(failed[i].items)), r.queue.attrs)
	}

	if len(undispatched) == 0 {
		return
	}

	// Handed straight back rather than left to lapse. These items were claimed
	// and never started, so there is nothing to wait out: a release with no delay
	// puts them in front of the next worker immediately, where a lapsing lease
	// would have parked them for the rest of it. The cause is recorded because
	// last_error is "why the last attempt handed this back", and "the runner
	// stopped before it started this one" is an honest answer that a reader
	// chasing a stalled item needs — the claim already spent an attempt on it.
	if err := r.queue.Release(ctx, 0, ErrRunnerStopped, undispatched...); err != nil {
		r.logKeys(undispatched).Error("releasing undispatched work queue items", err)
	}

	r.drainedCounter.Add(ctx, int64(len(undispatched)), r.queue.attrs)
}

// extendWhileRunning starts the heartbeat that keeps the claims on running
// handlers alive, and returns the function that stops it and waits for it.
//
// It is one statement per tick for the whole batch rather than one per item:
// the items were claimed together under one name, so they are extended together.
// An item whose handler has returned is out of the set before the next tick, so
// a finished item is never extended and never counted as lost.
func (r *Runner[K]) extendWhileRunning(ctx context.Context, running *inFlight[K]) func() {
	ctx, cancel := context.WithCancel(ctx)

	done := make(chan struct{})

	go func() {
		defer close(done)

		ticker := time.NewTicker(r.cfg.ExtendInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.extend(ctx, running.snapshot())
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// extend pushes out the leases on the items still being handled.
//
// A failure is logged rather than propagated: the work is already running, and
// the only thing a failed extension costs is the chance that this claim lapses
// before the next tick lands — which is the ordinary lease-expiry path, not a
// reason to stop handling the batch.
func (r *Runner[K]) extend(ctx context.Context, running []Item[K]) {
	if len(running) == 0 {
		return
	}

	// Bounded by the lease it is extending, because an extension that is still
	// in flight when the horizon it was pushing out has passed is a statement
	// with nothing left to preserve — and the next tick will ask again.
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Lease)
	defer cancel()

	held, err := r.queue.Extend(ctx, r.cfg.Lease, running...)
	if err != nil {
		// A cancellation is the heartbeat being stopped with a tick already in
		// flight, which is what happens when the last handler returns while this
		// statement is running. That is the loop finishing rather than a failure,
		// and the context it cancels is this package's own — the caller's was
		// detached from before the heartbeat started.
		if !stderrors.Is(err, context.Canceled) {
			r.logKeys(running).Error("extending work queue leases", err)
		}

		return
	}

	r.extendedCounter.Add(ctx, held, r.queue.attrs)

	if lost := int64(len(running)) - held; lost > 0 {
		r.lostLeaseCounter.Add(ctx, lost, r.queue.attrs)
	}
}

// failureGroup is the items one distinct handler error accounted for, and the
// one error recorded for all of them.
type failureGroup[K comparable] struct {
	cause error
	items []Item[K]
}

// inFlight is the set of items whose handlers are currently running, which is
// what the heartbeat extends.
//
// It is a slice rather than a map because it is read whole every tick and is
// bounded by Concurrency, which is small: the removal's linear scan is cheaper
// than the map a key type this package cannot constrain any further would need.
type inFlight[K comparable] struct {
	items []Item[K]

	mu sync.Mutex
}

func (f *inFlight[K]) add(item Item[K]) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.items = append(f.items, item)
}

func (f *inFlight[K]) remove(item Item[K]) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i := range f.items {
		if f.items[i].Key == item.Key && f.items[i].LeasedBy == item.LeasedBy {
			f.items = append(f.items[:i], f.items[i+1:]...)

			return
		}
	}
}

// snapshot copies the set, so the extension runs its statement without holding
// the lock a returning handler needs to leave.
func (f *inFlight[K]) snapshot() []Item[K] {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]Item[K](nil), f.items...)
}

// work runs the handler over a claimed batch, Concurrency at a time, and splits
// the result into what was handled, what failed, and what a cancellation stopped
// it from starting.
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
func (r *Runner[K]) work(
	ctx context.Context,
	items []Item[K],
	running *inFlight[K],
) (handled []Item[K], failed []failureGroup[K], undispatched []Item[K]) {
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	byMessage := make(map[string]int, len(items))
	slots := make(chan struct{}, r.cfg.Concurrency)

	for i := range items {
		slots <- struct{}{}

		// Cancellation stops handing out new work but never abandons what is
		// already running: the wait below is for those, and the writes that
		// record every one of them run on a detached context. What was never
		// started is handed back rather than left to lapse — see record.
		//
		// The check is after the slot rather than before it, so it is made at
		// the moment a handler frees one. Before it, the loop would check, find
		// the context live, and then park on a slot for however long the
		// running handlers take — deciding what to dispatch on information
		// that is stale by exactly the time it spends waiting.
		if ctx.Err() != nil {
			<-slots

			undispatched = append(undispatched, items[i:]...)

			break
		}

		wg.Add(1)

		go func(item Item[K]) {
			defer wg.Done()
			defer func() { <-slots }()

			running.add(item)
			defer running.remove(item)

			err := r.handle(ctx, item)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				at, seen := byMessage[err.Error()]
				if !seen {
					at = len(failed)
					byMessage[err.Error()] = at
					failed = append(failed, failureGroup[K]{cause: err})
				}

				failed[at].items = append(failed[at].items, item)

				return
			}

			handled = append(handled, item)
		}(items[i])
	}

	wg.Wait()

	return handled, failed, undispatched
}

// handle runs one item under its own span, containing whatever the handler does
// to itself.
func (r *Runner[K]) handle(ctx context.Context, item Item[K]) error {
	values := map[string]any{
		attemptKey:   item.Attempts,
		reclaimedKey: item.Reclaimed,
	}

	// The encoded key rather than the Go value, because it is what the row is
	// filed under: a reader who found this span by its error goes to the table
	// next, and anything else makes them guess at the rendering.
	if key, err := encodeKey(r.queue.codec, item.Key); err == nil {
		values[itemKeyKey] = key
	}

	ctx, op := r.o11y.Begin(ctx, observability.WithValues(values))
	defer op.End()

	started := time.Now()

	err := panicking.Contain(func() error { return r.handler(ctx, item) })

	r.durationHist.Record(ctx, float64(time.Since(started).Milliseconds()), r.queue.attrs)

	// The stack reaches the span before the PanicError is replaced, since the
	// sentinel that wraps it no longer carries one. It is diagnostic and belongs
	// where diagnostics are read; what reaches the row's last_error is the value.
	if pe, ok := stderrors.AsType[*panicking.PanicError](err); ok {
		op.SpanOnly(panicStackKey, string(pe.Stack))
		r.panickedCounter.Add(ctx, 1, r.queue.attrs)

		err = platformerrors.Wrapf(ErrHandlerPanicked, "%v", pe.Value)
	}

	if err != nil {
		return op.Error(err, "handling work queue item")
	}

	return nil
}

// logKeys returns the runner's logger carrying the encoded keys of a batch.
//
// A write that fails against a batch is the one place the keys are otherwise
// lost entirely: Complete and Release take the whole slice, so a failure says
// only that some items could not be reported on. It is bounded by the configured
// batch size, which is what makes putting the whole list on one line reasonable.
//
// A key that will not encode is skipped rather than rendered some other way. It
// cannot happen for a key that was just decoded out of the table, and a
// placeholder in the list would read as a key nobody can find.
func (r *Runner[K]) logKeys(items []Item[K]) logging.Logger {
	encoded := make([]string, 0, len(items))

	for i := range items {
		if key, err := encodeKey(r.queue.codec, items[i].Key); err == nil {
			encoded = append(encoded, key)
		}
	}

	return r.o11y.Logger().WithValue(itemKeysKey, encoded)
}
