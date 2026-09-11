package service

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/primandproper/platform-go/v14/operations"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// operationsRunner adapts an operations loop to Runner.
//
// It hosts the two loops operations ships — the Worker that claims work and the
// Watcher that pushes snapshots — because they are the only ones in this module
// whose Run takes a context and returns an error rather than blocking until
// Close, and the shape is not an oversight on either side: operations is stopped
// by cancelling the context it was given, which is the ordinary way to stop
// something that also has to be startable from a plain errgroup.
//
// The adapter lives here rather than in operations because the Run/Close pair is
// this package's convention, and a package that has no opinion about how it is
// hosted should not grow one to satisfy a host it does not import.
//
// It is deliberately thin. Nothing is buffered, nothing is retried, and Close
// waits for the pass in flight — which is what the operations worker already
// does when its context goes: it stops handing out new work and waits for the
// batch it claimed, on a detached context, so an operation is recorded rather
// than reclaimed and run twice.
type operationsRunner struct {
	// ctx is held rather than passed because Run and Close are separate calls
	// and neither takes one. It is created here so Close is safe before Run.
	ctx    context.Context
	cancel context.CancelFunc

	// run is the loop, held as a function because that is the whole of what
	// this adapter uses. Hosting a blocking, context-cancelled loop is not a
	// fact about operations, and holding the Worker would mean a Postgres
	// container to test twenty lines of goroutine plumbing.
	run func(context.Context) error

	// release is what this loop's owner has to be told besides the
	// cancellation, or nil for a loop the context is the whole of the stop for.
	// The watcher is why it exists: cancelling its context ends its polling but
	// leaves every subscriber holding a channel nothing will ever close or
	// deliver on again, and Watcher.Close is what ends those.
	release func() error

	// releaseErr is what release reported, kept because Close runs it once and
	// may be called any number of times.
	releaseErr error

	done chan struct{}

	// name is what this loop is called in the drain failure, since a shutdown
	// that gave up has no other way to say which of the two it was waiting on.
	name string

	// started records that Run was entered, so Close can tell a loop it must
	// wait for from one that was never started. Without it a service that fails
	// before Run — a profiler that will not start, which Service.Run handles by
	// shutting down what it has not started yet — spends its whole shutdown
	// budget waiting for a goroutine that does not exist, and then reports a
	// drain that never happened.
	//
	// The narrow race it leaves is not one: Close cancels before it reads this,
	// so a Run entered afterwards is handed a context that is already done and
	// returns without claiming anything.
	started atomic.Bool

	closeOnce sync.Once
}

var _ Runner = (*operationsRunner)(nil)

func newOperationsRunner(worker *operations.Worker) *operationsRunner {
	return newLoopRunner("worker", worker.Run)
}

// newWatcherRunner adapts an operations.Watcher, whose stop is two things
// rather than one.
//
// Cancelling the context ends the poll loop, which is all the worker needs; the
// watcher also holds every subscriber's channel, and a subscriber whose channel
// is never closed waits for a snapshot the process it was talking to is no
// longer running. Watcher.Close is what closes them, and it is idempotent, so
// running it here costs a service that closed its own watcher nothing.
func newWatcherRunner(watcher *operations.Watcher) *operationsRunner {
	r := newLoopRunner("watcher", watcher.Run)
	r.release = watcher.Close

	return r
}

func newLoopRunner(name string, run func(context.Context) error) *operationsRunner {
	ctx, cancel := context.WithCancel(context.Background())

	return &operationsRunner{
		name:   name,
		run:    run,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
}

// Run blocks running the loop until Close.
//
// The context is this adapter's own rather than the server's, for the reason
// Runner's documentation gives: a loop tied to the server's context stops the
// moment ingress does, which for operations is exactly when the last requests
// are still starting them.
func (r *operationsRunner) Run() {
	defer close(r.done)

	r.started.Store(true)

	// The error is the context's, every time — both loops return only when
	// their context is done and ride out everything else, logging as they go —
	// so there is nothing here to report that Close does not already know.
	_ = r.run(r.ctx) //nolint:errcheck // the only error it returns is the cancellation Close asked for
}

// Close stops the loop and waits for the pass in flight, up to ctx's deadline.
func (r *operationsRunner) Close(ctx context.Context) error {
	r.closeOnce.Do(func() {
		r.cancel()

		if r.release != nil {
			r.releaseErr = r.release()
		}
	})

	// Nothing was started, so nothing is draining, and the budget this would
	// otherwise spend belongs to the components that did start.
	if !r.started.Load() {
		return r.releaseErr
	}

	select {
	case <-r.done:
		return r.releaseErr
	case <-ctx.Done():
		return platformerrors.Join(
			r.releaseErr,
			platformerrors.Wrapf(ctx.Err(), "waiting for the operations %s to drain", r.name),
		)
	}
}
