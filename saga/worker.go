package saga

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/distributedlock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/panicking"
	"github.com/primandproper/platform-go/v13/retry"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// maxStoredErrorLength bounds a stored error rendering. A step that returns a
// provider error containing the request it choked on could otherwise write the
// customer's card details into the instance row.
const maxStoredErrorLength = 1024

// panicStackKey carries a contained panic's stack. Span-only: a stack trace is
// long, is attached to something already being reported as an error, and does
// not belong in every log aggregator's index.
const panicStackKey = "saga.panic_stack"

// StepResult is what the idempotency manager records for a step: the state the
// step left behind.
//
// It is exported because the Manager is constructed by the application —
// idempotency.NewManager[saga.StepResult](store, locker) — and its type
// parameter has to be nameable there. There is nothing in it to configure.
type StepResult struct {
	// State is the encoded saga state as the step left it.
	State []byte
}

// Worker advances claimed instances. It owns a goroutine started by Run and
// stopped by Close.
//
// It is deliberately not generic. One worker pool advances every saga in the
// process regardless of what state each carries, which is only possible because
// the Registry erased the state types at registration — see the definition type.
type Worker struct {
	store       Store
	registry    *Registry
	locker      distributedlock.ScopedLocker
	publisher   EventPublisher
	idempotency *idempotency.Manager[StepResult]
	clock       clock.Clock
	o11y        observability.Observer

	stop chan struct{}
	done chan struct{}

	stepCounter          metrics.Int64Counter
	stepFailureCounter   metrics.Int64Counter
	completedCounter     metrics.Int64Counter
	compensationsCounter metrics.Int64Counter
	compensatedCounter   metrics.Int64Counter
	stuckCounter         metrics.Int64Counter
	claimErrCounter      metrics.Int64Counter
	contendedCounter     metrics.Int64Counter
	stepHist             metrics.Float64Histogram
	advanceHist          metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read w.o11y.Logger() for the logger this worker actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg WorkerConfig

	stopOnce sync.Once
}

// NewWorker builds a Worker. It does not start it; call Run.
//
// ctx is used to validate the config and is not retained — Run takes its own.
//
// The locker is required and has no default. See ErrNilLocker: the lease alone
// stops two workers picking the same instance up, but a lease is a timestamp
// and a lease that lapses mid-pass is exactly the moment two workers would run
// the same step.
func NewWorker(
	ctx context.Context,
	cfg *WorkerConfig,
	store Store,
	registry *Registry,
	locker distributedlock.ScopedLocker,
	opts ...WorkerOption,
) (*Worker, error) {
	if cfg == nil {
		return nil, platformerrors.New("nil saga worker config provided")
	}

	if store == nil {
		return nil, ErrNilStore
	}

	if registry == nil {
		return nil, ErrNilRegistry
	}

	if locker == nil {
		return nil, ErrNilLocker
	}

	cfg.EnsureDefaults()

	w := &Worker{
		cfg:      *cfg,
		store:    store,
		registry: registry,
		locker:   locker,
		clock:    clock.NewClock(),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}

	if err := w.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating saga worker config")
	}

	w.o11y = observability.NewObserver(serviceName, w.logger, w.tracerProvider)

	if err := w.buildInstruments(); err != nil {
		return nil, err
	}

	return w, nil
}

// buildInstruments creates the Worker's metrics up front, so a misconfigured
// meter fails the constructor rather than the first cycle.
func (w *Worker) buildInstruments() error {
	mp := metrics.EnsureMetricsProvider(w.metricsProvider)

	var err error
	if w.stepCounter, err = mp.NewInt64Counter(serviceName + "_steps_completed"); err != nil {
		return platformerrors.Wrap(err, "creating steps completed counter")
	}
	if w.stepFailureCounter, err = mp.NewInt64Counter(serviceName + "_step_failures"); err != nil {
		return platformerrors.Wrap(err, "creating step failures counter")
	}
	if w.completedCounter, err = mp.NewInt64Counter(serviceName + "_instances_completed"); err != nil {
		return platformerrors.Wrap(err, "creating instances completed counter")
	}
	// "_compensations_started" rather than "_instances_compensating": this is a
	// monotonic counter, and the old name reads as a gauge — the number of
	// instances compensating right now. Metric names lock into dashboards and
	// alerts the moment they ship, so the name has to match what the instrument
	// actually is before the tag, not after.
	if w.compensationsCounter, err = mp.NewInt64Counter(serviceName + "_compensations_started"); err != nil {
		return platformerrors.Wrap(err, "creating compensations started counter")
	}
	if w.compensatedCounter, err = mp.NewInt64Counter(serviceName + "_instances_compensated"); err != nil {
		return platformerrors.Wrap(err, "creating instances compensated counter")
	}
	// The one to alert on. Everything else in this package is a saga working as
	// designed, including compensation; this is the counter that means a person
	// has to go and look at something.
	if w.stuckCounter, err = mp.NewInt64Counter(serviceName + "_instances_stuck"); err != nil {
		return platformerrors.Wrap(err, "creating instances stuck counter")
	}
	if w.claimErrCounter, err = mp.NewInt64Counter(serviceName + "_claim_errors"); err != nil {
		return platformerrors.Wrap(err, "creating claim errors counter")
	}
	if w.contendedCounter, err = mp.NewInt64Counter(serviceName + "_lock_contended"); err != nil {
		return platformerrors.Wrap(err, "creating lock contention counter")
	}
	if w.stepHist, err = mp.NewFloat64Histogram(serviceName + "_step_latency_ms"); err != nil {
		return platformerrors.Wrap(err, "creating step latency histogram")
	}
	if w.advanceHist, err = mp.NewFloat64Histogram(serviceName + "_advance_latency_ms"); err != nil {
		return platformerrors.Wrap(err, "creating advance latency histogram")
	}

	return nil
}

// Run is the worker loop. Like the other durable workers in this module it
// takes no context: tied to a server context it would stop advancing sagas
// while requests were still starting them, which is the one moment a
// half-finished saga is least welcome. The owner calls Close after the server
// has shut down.
//
// Run returns only after Close.
func (w *Worker) Run() {
	defer close(w.done)

	ctx := context.Background()

	ticker := w.clock.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.Chan():
			w.cycle(ctx)
		}
	}
}

// Close stops the worker and waits for the in-flight cycle to finish. Safe to
// call more than once.
//
// There is no final cycle on the way out. A pass can run for minutes and holds
// a lease that outlives the process, so the right thing at shutdown is to stop
// claiming and let another replica pick the work up — not to start a step that
// will be killed halfway through.
func (w *Worker) Close(ctx context.Context) error {
	_, op := w.o11y.Begin(ctx)
	defer op.End()

	w.stopOnce.Do(func() { close(w.stop) })

	select {
	case <-w.done:
	case <-ctx.Done():
		return op.Error(ctx.Err(), "waiting for saga worker to drain")
	}

	return nil
}

// cycle claims one batch and advances it. Errors are logged and counted rather
// than returned: there is no caller to hand them to, and the next cycle retries.
func (w *Worker) cycle(ctx context.Context) {
	now := w.clock.Now().UTC()

	claimed, err := w.store.Claim(ctx, now, w.cfg.BatchSize, now.Add(w.cfg.LeaseDuration))
	if err != nil {
		w.claimErrCounter.Add(ctx, 1)
		w.o11y.Logger().Error("claiming saga instances", err)

		return
	}

	if len(claimed) == 0 {
		return
	}

	ctx, op := w.o11y.Begin(ctx, observability.WithValue(claimedKey, len(claimed)))
	defer op.End()

	sem := make(chan struct{}, w.cfg.Concurrency)

	var wg sync.WaitGroup

	for _, inst := range claimed {
		sem <- struct{}{}

		wg.Go(func() {
			defer func() { <-sem }()

			w.advance(ctx, inst)
		})
	}

	wg.Wait()
}

// advance takes the instance lock and drives the saga as far as it will go.
//
// The lock is taken rather than waited for. If another worker holds it, this
// one has claimed an instance somebody else is already advancing — which means
// a lease lapsed mid-pass — and the useful thing to do is hand the lease back
// and move on, not block a goroutine for the length of somebody else's pass.
func (w *Worker) advance(ctx context.Context, inst *Record) {
	ctx, op := w.o11y.Begin(ctx, observability.WithValues(map[string]any{
		instanceIDKey: inst.ID,
		definitionKey: inst.Definition,
		statusKey:     string(inst.Status),
		stepIndexKey:  inst.CurrentStep,
		attemptsKey:   inst.Attempts,
	}))
	defer op.End()

	// Stopped where the pass ends rather than deferred: what follows the
	// recording is bookkeeping about the outcome, and a lock this worker did
	// not get is not a pass whose duration means anything.
	recordLatency := op.Time(ctx, w.clock, w.advanceHist, definitionAttr(inst.Definition))

	// Deliberately no timeout on the pass as a whole. Each step is bounded by
	// StepTimeout inside execute, and drive stops starting new ones once its
	// wall-clock budget is spent — so a pass lasts at most AdvanceTimeout plus
	// one step, which is what the lease and lock TTLs are validated against.
	//
	// A context deadline over the whole pass would cancel a step mid-flight and
	// then hand that same expired context to the write recording the failure,
	// which is how a saga comes to lose the record of why it stopped.
	acquired, err := w.locker.TryWithLock(ctx, w.cfg.LockKeyPrefix+inst.ID, func(ctx context.Context) error {
		return w.drive(ctx, op, inst)
	})

	recordLatency()

	if !acquired && err == nil {
		w.contendedCounter.Add(ctx, 1, definitionAttr(inst.Definition))
		op.Set("saga.lock_contended", true)

		// Hand the lease back rather than sitting on it. The worker that holds
		// the lock is making progress; this one would only keep the row out of
		// its own claim index until the lease lapsed.
		w.release(ctx, inst)

		return
	}

	if err != nil {
		// Everything recoverable has already been persisted by drive. What
		// reaches here is an unwritable database or a lost lock, and the lease
		// expiring is what makes the instance claimable again.
		w.o11y.Logger().WithValues(map[string]any{
			instanceIDKey: inst.ID,
			definitionKey: inst.Definition,
		}).Error("advancing saga instance", err)
	}
}

// drive runs steps until the instance rests: it finishes, it waits for a delay,
// a step fails and is rescheduled, or the pass runs out of budget.
func (w *Worker) drive(ctx context.Context, op observability.Operation, inst *Record) error {
	def, ok := w.registry.lookup(inst.Definition)
	if !ok {
		// A definition this build does not have is not one it can advance and
		// not one it can compensate. Marking it stuck is the honest answer: the
		// alternative is retrying until the budget runs out and then running a
		// compensation this process also does not have.
		return w.markStuck(ctx, inst, platformerrors.Wrapf(
			ErrUnknownDefinition, "saga definition %q is not registered in this process", inst.Definition,
		))
	}

	if !slices.Equal(def.stepNames, inst.StepNames) {
		return w.markStuck(ctx, inst, driftError(inst, def))
	}

	deadline := w.clock.Now().Add(w.cfg.AdvanceTimeout)

	var advanced int

	for {
		if inst.Status.Terminal() {
			op.Set(advancedKey, advanced).Set(terminalKey, true)

			return nil
		}

		if !w.clock.Now().Before(deadline) {
			// Out of budget with the saga still in flight. Hand it back so
			// another worker — or this one on the next cycle — carries on from
			// the cursor rather than waiting out the lease.
			op.Set(advancedKey, advanced)

			return w.store.Release(ctx, inst.ID, w.clock.Now().UTC())
		}

		more, err := w.step(ctx, def, inst)
		if err != nil {
			return err
		}

		advanced++

		if !more {
			op.Set(advancedKey, advanced)

			return nil
		}
	}
}

// step performs one unit of progress and reports whether the pass should carry
// on. Everything it does is persisted before it returns.
func (w *Worker) step(ctx context.Context, def *definition, inst *Record) (bool, error) {
	switch inst.Status {
	case StatusRunning:
		return w.runStep(ctx, def, inst)
	case StatusCompensating:
		return w.compensateStep(ctx, def, inst)
	case StatusCompleted, StatusCompensated, StatusStuck:
		return false, nil
	default:
		// A status this build does not know, written by one that did. Refusing
		// is the only safe move: guessing which phase it meant is guessing
		// whether to charge somebody or refund them.
		return false, w.markStuck(ctx, inst, platformerrors.Newf("unknown saga status %q", inst.Status))
	}
}

// runStep executes the step the cursor points at and moves the cursor forward.
func (w *Worker) runStep(ctx context.Context, def *definition, inst *Record) (bool, error) {
	idx := inst.CurrentStep

	if idx >= def.steps() {
		// Reachable when a definition's step list shrank and the drift check
		// somehow passed, and harmless: every step this instance knows about
		// has run.
		inst.Status = StatusCompleted

		return false, w.finishRunning(ctx, inst, def, idx)
	}

	state, err := w.execute(ctx, def, inst, idx, phaseDo)
	if err != nil {
		return w.failForward(ctx, def, inst, idx, err)
	}

	name := def.stepNames[idx]

	inst.State = state
	inst.CurrentStep = idx + 1
	inst.LastError = ""
	inst.Attempts = 0

	w.stepCounter.Add(ctx, 1, stepAttrs(inst.Definition, name, phaseDo))

	if inst.CurrentStep >= def.steps() {
		inst.Status = StatusCompleted

		return false, w.finishRunning(ctx, inst, def, idx)
	}

	// The next step's delay decides when this instance is claimable again, and
	// whether this pass carries on into it.
	delay := def.delays[inst.CurrentStep]

	now := w.clock.Now().UTC()

	if err = w.persist(ctx, inst, now.Add(delay),
		newEvent(EventStepCompleted, inst, name, idx, now, ""),
	); err != nil {
		return false, err
	}

	return delay == 0, nil
}

// finishRunning records a saga that ran every step.
func (w *Worker) finishRunning(ctx context.Context, inst *Record, def *definition, idx int) error {
	now := w.clock.Now().UTC()

	name := ""
	if idx < def.steps() {
		name = def.stepNames[idx]
	}

	events := []Event{
		newEvent(EventStepCompleted, inst, name, idx, now, ""),
		newEvent(EventCompleted, inst, "", inst.CurrentStep, now, ""),
	}

	if name == "" {
		events = events[1:]
	}

	if err := w.persist(ctx, inst, now, events...); err != nil {
		return err
	}

	w.completedCounter.Add(ctx, 1, definitionAttr(inst.Definition))

	return nil
}

// failForward handles a Do that returned an error: schedule the retry, or give
// up going forward and start unwinding.
func (w *Worker) failForward(ctx context.Context, def *definition, inst *Record, idx int, cause error) (bool, error) {
	name := def.stepNames[idx]

	w.stepFailureCounter.Add(ctx, 1, stepAttrs(inst.Definition, name, phaseDo))

	attempts := max(inst.Attempts, 1)

	if !w.exhausted(cause, attempts, phaseDo) {
		return false, w.reschedule(ctx, inst, attempts, phaseDo, cause)
	}

	now := w.clock.Now().UTC()

	// Compensation starts at the failed step, not the one before it. A Do that
	// returned an error may still have posted the charge before it failed, and
	// that half-applied effect is precisely what a saga exists to take back —
	// see Step.Undo.
	inst.Status = StatusCompensating
	inst.CurrentStep = idx
	inst.LastError = truncateError(cause)
	inst.Attempts = 0

	w.compensationsCounter.Add(ctx, 1, definitionAttr(inst.Definition))

	w.o11y.Logger().WithValues(map[string]any{
		instanceIDKey: inst.ID,
		definitionKey: inst.Definition,
		stepKey:       name,
		attemptsKey:   attempts,
	}).Info("saga step exhausted its attempts; compensating")

	if err := w.persist(ctx, inst, now,
		newEvent(EventCompensating, inst, name, idx, now, inst.LastError),
	); err != nil {
		return false, err
	}

	// Carry straight on into the compensation. The pass has budget left and the
	// steps being undone are the ones this worker just ran.
	return true, nil
}

// compensateStep runs the Undo the cursor points at and moves the cursor back.
func (w *Worker) compensateStep(ctx context.Context, def *definition, inst *Record) (bool, error) {
	idx := inst.CurrentStep

	if idx < 0 {
		return false, w.finishCompensating(ctx, inst, "", idx)
	}

	if idx >= def.steps() {
		// A cursor outside the step list cannot be unwound and must not be
		// guessed at.
		return false, w.markStuck(ctx, inst, platformerrors.Newf(
			"saga cursor %d is outside the %d steps of definition %q", idx, def.steps(), def.name,
		))
	}

	name := def.stepNames[idx]

	// A step with no Undo is skipped rather than failed: it declared that it
	// needs no compensation, and emitting an event for a compensation that did
	// not happen would be a lie in the stream operators read.
	if !def.compensates[idx] {
		inst.CurrentStep = idx - 1
		inst.Attempts = 0

		if inst.CurrentStep < 0 {
			return false, w.finishCompensating(ctx, inst, "", inst.CurrentStep)
		}

		return true, w.persist(ctx, inst, w.clock.Now().UTC())
	}

	state, err := w.execute(ctx, def, inst, idx, phaseUndo)
	if err != nil {
		w.stepFailureCounter.Add(ctx, 1, stepAttrs(inst.Definition, name, phaseUndo))

		attempts := max(inst.Attempts, 1)

		if !w.exhausted(err, attempts, phaseUndo) {
			return false, w.reschedule(ctx, inst, attempts, phaseUndo, err)
		}

		return false, w.markStuck(ctx, inst, platformerrors.Wrapf(
			ErrCompensationFailed, "saga step %q could not be compensated: %v", name, err,
		))
	}

	inst.State = state
	inst.CurrentStep = idx - 1
	inst.Attempts = 0

	// LastError is deliberately not cleared on the way back. It holds why the
	// saga gave up going forward, and that is the first thing anybody asks
	// about a compensated instance — an instance that unwound cleanly and says
	// nothing about why is a row that has forgotten the only interesting fact
	// about itself.

	w.stepCounter.Add(ctx, 1, stepAttrs(inst.Definition, name, phaseUndo))

	if inst.CurrentStep < 0 {
		return false, w.finishCompensating(ctx, inst, name, idx)
	}

	now := w.clock.Now().UTC()

	// Delays are not honored on the way back. A delay says "wait before doing
	// this"; unwinding is not doing it, and a compensation that waited out the
	// same schedule would leave the effects it is undoing in place for as long
	// as the saga was originally meant to take.
	return true, w.persist(ctx, inst, now,
		newEvent(EventStepCompensated, inst, name, idx, now, ""),
	)
}

// finishCompensating records a saga that unwound every step.
func (w *Worker) finishCompensating(ctx context.Context, inst *Record, name string, idx int) error {
	now := w.clock.Now().UTC()

	inst.Status = StatusCompensated

	events := make([]Event, 0, 2)
	if name != "" {
		events = append(events, newEvent(EventStepCompensated, inst, name, idx, now, ""))
	}

	events = append(events, newEvent(EventCompensated, inst, "", inst.CurrentStep, now, inst.LastError))

	if err := w.persist(ctx, inst, now, events...); err != nil {
		return err
	}

	w.compensatedCounter.Add(ctx, 1, definitionAttr(inst.Definition))

	return nil
}

// markStuck moves an instance to StatusStuck, recording which phase it was in
// so Resume can put it back there.
func (w *Worker) markStuck(ctx context.Context, inst *Record, cause error) error {
	now := w.clock.Now().UTC()

	inst.ResumeStatus = inst.Status
	inst.Status = StatusStuck
	inst.LastError = truncateError(cause)

	w.stuckCounter.Add(ctx, 1, definitionAttr(inst.Definition))

	// Error level, and the only Error this worker logs for a state it wrote on
	// purpose. Something is half-done, this process has run out of ways to undo
	// it, and nothing else in the system is going to notice.
	w.o11y.Logger().WithValues(map[string]any{
		instanceIDKey: inst.ID,
		definitionKey: inst.Definition,
		stepIndexKey:  inst.CurrentStep,
		fromStatusKey: string(inst.ResumeStatus),
	}).Error("saga instance is stuck and needs an operator", cause)

	return w.persist(ctx, inst, now,
		newEvent(EventStuck, inst, "", inst.CurrentStep, now, inst.LastError),
	)
}

// reschedule records a step that failed and will be tried again.
func (w *Worker) reschedule(ctx context.Context, inst *Record, attempts int, phase string, cause error) error {
	inst.Attempts = attempts

	nextAttempt := w.clock.Now().UTC().Add(retrycfg.ScheduledDelayFor(w.cfg.budgetFor(phase), attempts))

	w.o11y.Logger().WithValues(map[string]any{
		instanceIDKey:  inst.ID,
		definitionKey:  inst.Definition,
		stepIndexKey:   inst.CurrentStep,
		phaseKey:       phase,
		attemptsKey:    attempts,
		nextAttemptKey: nextAttempt,
	}).Info("saga step failed, retry scheduled")

	return w.store.Reschedule(ctx, inst.ID, attempts, nextAttempt, truncateError(cause), w.clock.Now().UTC())
}

// persist writes the instance's position and its lifecycle events in one
// transaction.
//
// One transaction, not two, and that is the point of the EventPublisher seam
// taking an executor. An event that commits without the advance announces a
// step that did not happen; an advance that commits without its event leaves a
// subscriber waiting for a saga that has already finished.
func (w *Worker) persist(ctx context.Context, inst *Record, nextAttempt time.Time, events ...Event) error {
	inst.UpdatedAt = w.clock.Now().UTC()

	return w.store.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		if err := w.store.Advance(ctx, q, inst, nextAttempt); err != nil {
			return err
		}

		return publish(ctx, w.publisher, q, events...)
	})
}

// release hands a lease back without changing anything else.
func (w *Worker) release(ctx context.Context, inst *Record) {
	if err := w.store.Release(ctx, inst.ID, w.clock.Now().UTC()); err != nil {
		w.o11y.Logger().WithValue(instanceIDKey, inst.ID).Error("releasing saga instance lease", err)
	}
}

// execute runs one step's Do or Undo, under its own timeout, span, and — when
// one is configured — idempotency key.
func (w *Worker) execute(
	ctx context.Context,
	def *definition,
	inst *Record,
	idx int,
	phase string,
) (json.RawMessage, error) {
	ctx, op := w.o11y.Begin(ctx)
	defer op.End()

	name := def.stepNames[idx]

	op.SetValues(map[string]any{
		instanceIDKey: inst.ID,
		definitionKey: inst.Definition,
		stepKey:       name,
		stepIndexKey:  idx,
		phaseKey:      phase,
		attemptsKey:   inst.Attempts,
	})

	ctx, cancel := context.WithTimeout(ctx, w.cfg.StepTimeout)
	defer cancel()

	defer op.Time(ctx, w.clock, w.stepHist, stepAttrs(inst.Definition, name, phase))()

	apply := def.do
	if phase == phaseUndo {
		apply = def.undo
	}

	// The state the step sees is the state that was persisted, decoded fresh.
	// A step that runs after a crash therefore sees exactly what it would have
	// seen if the crash had not happened.
	state := inst.State

	run := func(ctx context.Context) (*StepResult, error) {
		var out json.RawMessage

		// Somebody else's code, running in our goroutine. A nil map access in
		// one application's payment client should cost that saga's step, not
		// every other instance in the batch.
		err := panicking.Contain(func() error {
			var applyErr error
			out, applyErr = apply(ctx, idx, state)

			return applyErr
		})
		if err != nil {
			return nil, containedPanic(op, err)
		}

		return &StepResult{State: out}, nil
	}

	if w.idempotency == nil {
		result, err := run(ctx)
		if err != nil {
			return nil, op.Error(err, "executing saga step %q", name)
		}

		return result.State, nil
	}

	result, err := w.idempotency.Do(ctx, w.stepKey(inst.ID, phase, name), w.stepFingerprint(inst, phase, name), run)
	if err != nil {
		return nil, op.Error(err, "executing saga step %q", name)
	}

	op.Set(replayedKey, result.Replayed)

	if result.Value == nil {
		// A recorded nil, which the manager only produces for a step that
		// returned one. Nothing to decode; the state is unchanged.
		return state, nil
	}

	return result.Value.State, nil
}

// stepKey renders the idempotency key for one (instance, phase, step).
//
// The attempt number is deliberately absent. A crash-and-resume becomes attempt
// two, and a fresh key per attempt would re-execute exactly the billable work
// the key exists to suppress. Dropping it is safe because Manager.Do only
// commits successful results: a genuinely failed step releases its claim, so
// the next attempt re-executes under the same key. Retry-after-error and
// replay-after-crash are distinguished by the store, not by the key.
func (w *Worker) stepKey(instanceID, phase, step string) idempotency.Key {
	return idempotency.Key(w.cfg.IdempotencyKeyPrefix + instanceID + ":" + phase + ":" + step)
}

// stepFingerprint renders the fingerprint for one step.
//
// It must be stable across attempts of the same step, and it must differ
// between steps that could share a key — which they cannot, since the key
// already names the instance and the step. So it identifies what the key is
// for, and its job here is to be the same on every attempt rather than to
// discriminate: a fingerprint derived from the state would change the moment an
// earlier step mutated it, turning every resumption into a mismatch.
func (w *Worker) stepFingerprint(inst *Record, phase, step string) idempotency.Fingerprint {
	return idempotency.Fingerprint(inst.Definition + ":" + phase + ":" + step)
}

// exhausted reports whether a failure has used up its phase's budget.
func (w *Worker) exhausted(cause error, attempts int, phase string) bool {
	if errors.Is(cause, retry.ErrUnretryable) {
		return true
	}

	return uint(attempts) >= w.cfg.budgetFor(phase).MaxAttempts
}

// containedPanic turns a panic that panicking.Contain caught into this
// package's sentinel, putting the stack on the span first — the wrapped
// sentinel no longer carries it. Anything that is not a contained panic is
// returned untouched.
func containedPanic(op observability.Operation, err error) error {
	pe, ok := errors.AsType[*panicking.PanicError](err)
	if !ok {
		return err
	}

	op.SpanOnly(panicStackKey, string(pe.Stack))

	return platformerrors.Wrapf(ErrStepPanicked, "%v", pe.Value)
}

// stepAttrs labels a measurement with its definition, step, and phase.
// Cardinality is bounded by the registry, which is a fixed list written at
// wiring time.
func stepAttrs(definitionName, step, phase string) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String(definitionKey, definitionName),
		attribute.String(stepKey, step),
		attribute.String(phaseKey, phase),
	)
}

// truncateError renders an error for storage, bounded.
func truncateError(err error) string {
	return platformerrors.TruncateError(err, maxStoredErrorLength)
}
