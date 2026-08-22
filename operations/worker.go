package operations

import (
	"context"
	stderrors "errors"
	"sync"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/panicking"
	"github.com/primandproper/platform-go/v13/workqueue"
)

// panicStackKey carries a contained panic's stack to the span, and only to the
// span. A stack is diagnostic and belongs where diagnostics are read; the row a
// client polls gets CodePanic and a message.
const panicStackKey = "operations.panic_stack"

// workerName scopes the worker's spans and logger, distinct from the service's
// so a trace of an operation running is not indistinguishable from a trace of
// one being started.
const workerName = serviceName + "_worker"

// Worker is the claim-run-finish loop over the operations queue.
//
// It holds no state between passes and owns no goroutine until Run is called.
// Run blocks; stop it by cancelling its context.
//
// One Worker runs every kind in the registry. It is not generic and does not
// need to be — see Definition on where the request type is bound.
type Worker struct {
	store    Store
	queue    *workqueue.Queue[string]
	registry *Registry
	o11y     observability.Observer

	claimedCounter   metrics.Int64Counter
	succeededCounter metrics.Int64Counter
	failedCounter    metrics.Int64Counter
	cancelledCounter metrics.Int64Counter
	retriedCounter   metrics.Int64Counter
	lostLeaseCounter metrics.Int64Counter

	durationHist metrics.Float64Histogram

	cfg WorkerConfig
}

// NewWorker builds a Worker over an existing store, queue, and registry.
//
// The queue is shared with the Service rather than built here, for the reason
// NewService gives: a process that runs operations usually starts them too, and
// two Queue values over one table merge nothing.
func NewWorker(
	ctx context.Context,
	cfg *WorkerConfig,
	store Store,
	queue *workqueue.Queue[string],
	registry *Registry,
	opts ...WorkerOption,
) (*Worker, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}
	if store == nil {
		return nil, ErrNilStore
	}
	if queue == nil {
		return nil, ErrNilQueue
	}
	if registry == nil {
		return nil, ErrNilRegistry
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating operations worker config")
	}

	o := newWorkerOptions(opts)

	w := &Worker{
		cfg:      *cfg,
		store:    store,
		queue:    queue,
		registry: registry,
		o11y:     observability.NewObserver(workerName, o.logger, o.tracerProvider),
	}
	if err := w.buildInstruments(metrics.EnsureMetricsProvider(o.metricsProvider)); err != nil {
		return nil, err
	}

	return w, nil
}

// buildInstruments creates every counter the worker owns, so a failure to build
// one is reported at construction rather than discovered as a silently missing
// series.
func (w *Worker) buildInstruments(mp metrics.Provider) error {
	var err error

	if w.claimedCounter, err = mp.NewInt64Counter(workerName + "_claimed"); err != nil {
		return platformerrors.Wrap(err, "creating operations claimed counter")
	}

	if w.succeededCounter, err = mp.NewInt64Counter(workerName + "_succeeded"); err != nil {
		return platformerrors.Wrap(err, "creating operations succeeded counter")
	}

	if w.failedCounter, err = mp.NewInt64Counter(workerName + "_failed"); err != nil {
		return platformerrors.Wrap(err, "creating operations failed counter")
	}

	if w.cancelledCounter, err = mp.NewInt64Counter(workerName + "_cancelled"); err != nil {
		return platformerrors.Wrap(err, "creating operations cancelled counter")
	}

	if w.retriedCounter, err = mp.NewInt64Counter(workerName + "_retried"); err != nil {
		return platformerrors.Wrap(err, "creating operations retried counter")
	}

	// The one that says the lease is mis-sized. A worker whose Runner reported
	// progress and was still reclaimed is one whose ProgressInterval is not
	// keeping up with its Lease, or whose Runner went quiet for longer than the
	// lease — and the work was done twice either way.
	if w.lostLeaseCounter, err = mp.NewInt64Counter(workerName + "_leases_lost"); err != nil {
		return platformerrors.Wrap(err, "creating operations lost lease counter")
	}

	if w.durationHist, err = mp.NewFloat64Histogram(workerName + "_duration_ms"); err != nil {
		return platformerrors.Wrap(err, "creating operations duration histogram")
	}

	return nil
}

// Run claims and runs operations until ctx is done, then returns ctx.Err
// wrapped.
//
// A pass that claims a full batch goes straight round again — there is more
// waiting, and sleeping between full batches would pace a backlog at one batch
// per poll. Anything less waits, which is where a wakeup earns its keep.
//
// Nothing short of a cancelled context stops it. A failed claim is logged and
// slept off: the database being unreachable for a minute is an outage to ride
// out, not a reason for a fleet to stop running operations when it comes back.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return platformerrors.Wrap(err, "running the operations worker")
		}

		claimed, err := w.pass(ctx)
		if err != nil {
			// Not returned: see the method comment. The wait below is what keeps
			// a persistent failure from becoming a spin.
			w.o11y.Logger().Error("running claimed operations", err)
		}

		if err == nil && claimed >= w.cfg.Batch {
			continue
		}

		if waitErr := w.queue.Wait(ctx, w.cfg.Poll); waitErr != nil {
			return platformerrors.Wrap(waitErr, "running the operations worker")
		}
	}
}

// pass claims one batch and runs it, reporting how many items it claimed.
func (w *Worker) pass(ctx context.Context) (int, error) {
	// The queue lease covers dispatch, not the work. It is deliberately the same
	// length as the row's lease so that a worker which dies takes about as long
	// to have both taken away, but nothing rests on the two agreeing: the row's
	// guarded Begin is what decides who runs an operation, and a queue lease that
	// lapses early costs a wasted claim rather than a second execution.
	items, err := w.queue.Claim(ctx, w.cfg.Batch, w.cfg.Lease)
	if err != nil {
		return 0, err
	}

	if len(items) == 0 {
		return 0, nil
	}

	w.claimedCounter.Add(ctx, int64(len(items)))

	var wg sync.WaitGroup

	slots := make(chan struct{}, w.cfg.Concurrency)

	for i := range items {
		// Cancellation stops handing out new work but never abandons what is
		// already running: the loop below waits for it, and the writes that
		// record it run on a detached context. That is why this pass still
		// reports success — the batch it claimed is being finished, not dropped.
		if stopping(ctx) {
			break
		}

		slots <- struct{}{}

		wg.Add(1)

		go func(item workqueue.Item[string]) {
			defer wg.Done()
			defer func() { <-slots }()

			w.execute(ctx, item)
		}(items[i])
	}

	wg.Wait()

	return len(items), nil
}

// outcome is what one execution decided, and the queue write it implies.
type outcome struct {
	opErr  *Error
	result *Result
	state  State

	// retry means the operation goes back to pending and the queue item is
	// released with a delay rather than completed.
	retry bool

	// abandon means this worker is no longer entitled to record anything: the
	// row moved on without us. The queue item is completed regardless, because
	// whoever owns the operation now has their own item.
	abandon bool
}

// execute runs one claimed operation end to end.
//
//nolint:cyclop // the branches are the state machine; splitting them hides it.
func (w *Worker) execute(ctx context.Context, item workqueue.Item[string]) {
	ctx, span := w.o11y.Begin(ctx, observability.WithValues(map[string]any{
		operationIDKey: item.Key,
		attemptsKey:    item.Attempts,
	}))
	defer span.End()

	// Every write below runs on a context detached from the worker's, so a
	// shutdown arriving mid-operation still records what happened. Without it a
	// clean deploy would leave every in-flight operation to be reclaimed and run
	// again by whoever comes back up — turning the ordinary case into the
	// duplicate-execution case.
	writeCtx, cancelWrites := context.WithTimeout(context.WithoutCancel(ctx), w.cfg.Lease)
	defer cancelWrites()

	op, err := w.store.Begin(writeCtx, item.Key, item.Attempts, w.cfg.Lease)
	if err != nil {
		// Not ours to run: finished by somebody else, cancelled, or still leased
		// by a worker that has not given it up. The queue item is completed
		// either way — whoever owns the operation has their own item, and
		// leaving this one to lapse would have us claim it again on the next
		// pass and reach the same conclusion.
		w.complete(writeCtx, item.Key)

		if !stderrors.Is(err, ErrOperationNotFound) {
			span.Acknowledge(err, "beginning operation")
		}

		return
	}

	span.Set(kindKey, op.Kind).Set(ownerKey, op.Owner)

	result := w.run(ctx, writeCtx, span, op, item)

	switch {
	case result.abandon:
		w.lostLeaseCounter.Add(ctx, 1, kindAttr(op.Kind))
		w.complete(writeCtx, item.Key)

		return

	case result.retry:
		if err = w.store.Release(writeCtx, op.ID, result.opErr); err != nil {
			span.Acknowledge(err, "releasing operation for retry")
		}

		if err = w.queue.Release(writeCtx, w.cfg.RetryDelay, errorOf(result.opErr), item.Key); err != nil {
			span.Acknowledge(err, "releasing operation queue item")
		}

		w.retriedCounter.Add(ctx, 1, kindAttr(op.Kind))

		return
	}

	if err = w.store.Finish(writeCtx, op.ID, result.state, result.result, result.opErr,
		result.state == StateSucceeded); err != nil {
		// Acknowledged rather than returned: the work already ran, and the only
		// remaining question is whether the row records it. The store has logged
		// the guard miss with everything needed to find the operation by hand.
		span.Acknowledge(err, "recording operation outcome")
	}

	w.complete(writeCtx, item.Key)
	w.countOutcome(ctx, op, result.state)
}

// run executes the registered Runner under a reporter, and turns whatever comes
// back into an outcome.
//
//nolint:cyclop // as above: this is the classification table, and it reads as one.
func (w *Worker) run(
	ctx, writeCtx context.Context,
	span observability.Operation,
	op *Operation,
	item workqueue.Item[string],
) outcome {
	bound, err := w.registry.lookup(op.Kind)
	if err != nil {
		// A kind this build does not register is a hard failure rather than a
		// retry. The name vanished because somebody deleted or renamed it, and
		// retrying a name nothing will ever answer to burns the whole attempt
		// budget arriving at the same place a good deal later.
		span.Logger().WithValue(kindKey, op.Kind).
			WithValue("operations.registered_kinds", w.registry.Kinds()).
			Error("no runner registered for operation kind", err)

		return outcome{
			state: StateFailed,
			opErr: &Error{Code: CodeUnknownKind, Message: err.Error()},
		}
	}

	// item.Attempts is the queue's count, incremented by the claim before this
	// attempt began — so it is how many attempts have been made, this one
	// included, which is the same number the retry decision below compares.
	rep := newReporter(w.store, w.o11y.Logger(), op, w.cfg.Lease, w.cfg.ProgressInterval, Attempt{
		ID:     op.ID,
		Number: item.Attempts,
		Final:  item.Attempts >= w.maxAttempts(bound),
	})

	go rep.run(ctx)

	started := time.Now()

	result, runErr := runContained(ctx, bound, op.Request, rep)

	// The stack reaches the span before classify replaces the error with a
	// message an API client will read. A stack is a map of this build's
	// internals and belongs where diagnostics are read, not on the row.
	if pe, ok := stderrors.AsType[*panicking.PanicError](runErr); ok {
		span.SpanOnly(panicStackKey, string(pe.Stack))
	}

	// Closed before anything is decided, so the last thing the Runner said is on
	// the row before its outcome is — and so the flush that records it is the one
	// that learns whether we still hold the operation.
	rep.close(writeCtx)

	w.durationHist.Record(ctx, float64(time.Since(started).Milliseconds()), kindAttr(op.Kind))
	span.Set(durationKey, time.Since(started).Milliseconds())

	if rep.lostLease() {
		return outcome{abandon: true}
	}

	// Cancellation wins over both success and failure, and it wins on purpose. A
	// Runner that stopped early because it was asked to may return nil (it
	// finished tidily) or an error (it gave up mid-unit), and recording either as
	// what it looks like would report an export as complete when it is partial,
	// or as failed when nothing went wrong.
	if isCancelled(rep) {
		opErr := (*Error)(nil)
		if runErr != nil {
			opErr = &Error{Code: CodeCancelled, Message: runErr.Error()}
		}

		return outcome{state: StateCancelled, opErr: opErr}
	}

	if runErr == nil {
		return outcome{state: StateSucceeded, result: result}
	}

	opErr := classify(runErr)

	span.Acknowledge(runErr, "running operation")

	// Retryable, and there is budget left. attempts is the queue's count, which
	// the claim incremented before this attempt began — so item.Attempts is how
	// many attempts have been *made*, this one included.
	if opErr.Retryable && item.Attempts < w.maxAttempts(bound) {
		return outcome{retry: true, opErr: opErr}
	}

	if opErr.Retryable {
		// Out of budget. The code is rewritten so the row says why it stopped
		// rather than repeating the last symptom as though it were the reason,
		// and the message keeps the symptom, which is what anybody reading it
		// actually wants.
		opErr = &Error{
			Code:      CodeAttemptsExhausted,
			Message:   opErr.Message,
			Retryable: true,
		}
	}

	return outcome{state: StateFailed, opErr: opErr}
}

// stopping reports whether the worker's context has been cancelled.
//
// It is a named helper rather than an inline ctx.Err() check because the caller
// goes on to return a nil error, and a bare `if ctx.Err() != nil` beside a
// `return nil` reads — to a human and to a linter — as swallowing a failure. It
// is not: the batch already claimed is still being finished.
func stopping(ctx context.Context) bool {
	return ctx.Err() != nil
}

// maxAttempts resolves the attempt ceiling for a kind: its own, or the worker's.
func (w *Worker) maxAttempts(bound *runner) int {
	if bound.maxAttempts > 0 {
		return bound.maxAttempts
	}

	return w.cfg.MaxAttempts
}

// runContained executes a Runner with its panics contained.
//
// Somebody else's code running in our goroutine should cost its own operation,
// not every other one in the batch — and not the worker loop, which is the only
// thing keeping the rest of the fleet's work moving.
func runContained(
	ctx context.Context,
	bound *runner,
	request []byte,
	rep Reporter,
) (result *Result, err error) {
	err = panicking.Contain(func() error {
		var runErr error
		result, runErr = bound.run(ctx, request, rep)

		return runErr
	})

	return result, err
}

// classify turns a Runner's error into the Error the row records.
func classify(err error) *Error {
	if pe, ok := stderrors.AsType[*panicking.PanicError](err); ok {
		return &Error{
			Code: CodePanic,
			// The value, not the stack. A panic value is a short description of
			// what broke; a stack is a map of this build's internals, and this
			// message reaches API clients.
			Message:   platformerrors.Wrapf(ErrRunnerPanicked, "%v", pe.Value).Error(),
			Retryable: false,
		}
	}

	code := codeOf(err)
	if code == "" {
		code = CodeInternal
	}

	return &Error{
		Code:      code,
		Message:   err.Error(),
		Retryable: !IsUnretryable(err),
	}
}

// isCancelled reports whether the reporter's cancellation channel is closed.
func isCancelled(rep *reporter) bool { return Cancelled(rep) }

// errorOf renders a structured Error back into a Go error, for the queue's own
// last_error column. A queue item's error is an operator's breadcrumb rather
// than anything a client reads, so the rendering being lossy is fine.
func errorOf(opErr *Error) error {
	if opErr == nil {
		return nil
	}

	return platformerrors.Newf("%s: %s", opErr.Code, opErr.Message)
}

// complete retires the queue item. A failure is logged rather than propagated:
// the lease lapses, the item comes back, and the guarded Begin refuses it
// against a terminal row — a duplicate claim rather than a duplicate execution.
func (w *Worker) complete(ctx context.Context, key string) {
	if err := w.queue.Complete(ctx, key); err != nil {
		w.o11y.Logger().WithValue(operationIDKey, key).Error("completing operation queue item", err)
	}
}

// countOutcome records the terminal state against the kind.
func (w *Worker) countOutcome(ctx context.Context, op *Operation, state State) {
	attrs := kindAttr(op.Kind)

	switch state {
	case StateSucceeded:
		w.succeededCounter.Add(ctx, 1, attrs)
	case StateFailed:
		w.failedCounter.Add(ctx, 1, attrs)
	case StateCancelled:
		w.cancelledCounter.Add(ctx, 1, attrs)
	case StatePending, StateRunning:
		// Not reachable from here — execute only calls this after a terminal
		// write — and enumerated rather than defaulted so that adding a state
		// fails the exhaustiveness check instead of silently counting nothing.
	}
}
