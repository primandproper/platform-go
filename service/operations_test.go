package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/operations"
	operationsmock "github.com/primandproper/platform-go/v14/operations/mock"

	"github.com/primandproper/primitives-go/v2/database"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// blockingLoop is a Worker.Run in shape: it blocks until its context is done,
// and reports what it was given back to the test.
type blockingLoop struct {
	entered chan struct{}
	linger  time.Duration
	calls   atomic.Int64
}

func newBlockingLoop(linger time.Duration) *blockingLoop {
	return &blockingLoop{entered: make(chan struct{}, 1), linger: linger}
}

func (l *blockingLoop) run(ctx context.Context) error {
	l.calls.Add(1)
	l.entered <- struct{}{}

	<-ctx.Done()

	// A pass in flight, finishing after the cancellation rather than with it —
	// which is the thing Close's deadline is a budget for.
	time.Sleep(l.linger)

	return ctx.Err()
}

// awaitEntry blocks until the loop reports that it is running.
func (l *blockingLoop) awaitEntry(t *testing.T) {
	t.Helper()

	select {
	case <-l.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the loop was never entered")
	}
}

func TestOperationsRunner_Run(T *testing.T) {
	T.Parallel()

	T.Run("blocks until Close, and Close waits for the pass in flight", func(t *testing.T) {
		t.Parallel()

		loop := newBlockingLoop(50 * time.Millisecond)
		runner := newLoopRunner("worker", loop.run)

		go runner.Run()

		loop.awaitEntry(t)

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		start := time.Now()
		must.NoError(t, runner.Close(ctx))

		// Returning before the loop does would hand the process back to a
		// caller that is about to close the database under a worker still
		// recording the operation it claimed.
		test.GreaterEq(t, 50*time.Millisecond, time.Since(start))
		test.EqOp(t, int64(1), loop.calls.Load())
	})

	T.Run("reports a loop that outlasts the budget", func(t *testing.T) {
		t.Parallel()

		loop := newBlockingLoop(30 * time.Second)
		runner := newLoopRunner("worker", loop.run)

		go runner.Run()

		loop.awaitEntry(t)

		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		err := runner.Close(ctx)

		// Shutdown keeps going past a loop that will not stop, so this has to be
		// an error it can attribute rather than a wait it cannot end.
		must.Error(t, err)
		test.ErrorIs(t, err, context.DeadlineExceeded)
		test.StrContains(t, err.Error(), "waiting for the operations worker to drain")
	})

	T.Run("closes once however many times it is called", func(t *testing.T) {
		t.Parallel()

		loop := newBlockingLoop(0)
		runner := newLoopRunner("worker", loop.run)

		go runner.Run()

		loop.awaitEntry(t)

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		must.NoError(t, runner.Close(ctx))
		must.NoError(t, runner.Close(ctx))

		test.EqOp(t, int64(1), loop.calls.Load())
	})
}

func TestOperationsRunner_Close(T *testing.T) {
	T.Parallel()

	T.Run("returns at once when the loop was never started", func(t *testing.T) {
		t.Parallel()

		loop := newBlockingLoop(0)
		runner := newLoopRunner("worker", loop.run)

		// Service.Run shuts down what it has not started yet — a profiler that
		// will not start is the path — and a runner that waits there spends the
		// whole shutdown budget on a goroutine that does not exist, then
		// reports a drain that never happened.
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		start := time.Now()

		must.NoError(t, runner.Close(ctx))
		test.Less(t, time.Second, time.Since(start))
	})

	T.Run("a loop started after Close stops on its own", func(t *testing.T) {
		t.Parallel()

		loop := newBlockingLoop(0)
		runner := newLoopRunner("worker", loop.run)

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		must.NoError(t, runner.Close(ctx))

		// The race Close's early return leaves open, which is not one: it
		// cancels before it reads whether the loop started, so a Run entered
		// afterwards is handed a context that is already done.
		done := make(chan struct{})

		go func() {
			defer close(done)

			runner.Run()
		}()

		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("a loop started after Close never returned")
		}
	})
}

// The adapter production wires: the worker's own loop, reached through the
// constructor Service.New calls, rather than a function the test supplied.
func TestNewOperationsRunner(t *testing.T) {
	t.Parallel()

	runner := newOperationsRunner(&operations.Worker{})

	must.NotNil(t, runner.run)
	must.NotNil(t, runner.ctx)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	must.NoError(t, runner.Close(ctx))
	test.ErrorIs(t, runner.ctx.Err(), context.Canceled)
}

// The adapter production wires for the watcher: the watcher's own loop, reached
// through the constructor Service.New calls.
func TestNewWatcherRunner(T *testing.T) {
	T.Parallel()

	// A store answering Get with one non-terminal operation, which is the whole
	// of what Watch needs to hand back a live subscription.
	newWatcher := func(t *testing.T) *operations.Watcher {
		t.Helper()

		store := &operationsmock.StoreMock{
			GetFunc: func(
				context.Context,
				database.SQLQueryExecutor,
				tenancy.Scope,
				string,
			) (*operations.Operation, error) {
				return &operations.Operation{ID: "op-1", State: operations.StateRunning, Revision: 1}, nil
			},
		}

		watcher, err := operations.NewWatcher(t.Context(), &operations.WatcherConfig{},
			&databasemock.ClientMock{
				ReaderFunc: func() database.SQLQueryExecutor { return nil },
			}, store)
		must.NoError(t, err)

		return watcher
	}

	T.Run("closes the subscriptions cancelling the context alone would strand", func(t *testing.T) {
		t.Parallel()

		watcher := newWatcher(t)
		runner := newWatcherRunner(watcher)

		go runner.Run()

		updates, err := watcher.Watch(t.Context(), tenancy.Global(), "op-1")
		must.NoError(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		must.NoError(t, runner.Close(ctx))

		// The first snapshot is already buffered, so the channel is drained
		// rather than read once: what is under test is that it ends, not what
		// it carried.
		drained := make(chan struct{})

		go func() {
			defer close(drained)

			for range updates { //nolint:revive // draining to the close is the assertion
			}
		}()

		select {
		case <-drained:
		case <-time.After(30 * time.Second):
			t.Fatal("the subscription outlived the runner that owned it")
		}

		// And the other half of Watcher.Close: a subscriber arriving after
		// shutdown is told so rather than handed a channel nothing will ever
		// write to.
		_, err = watcher.Watch(t.Context(), tenancy.Global(), "op-1")
		test.ErrorIs(t, err, operations.ErrWatcherClosed)
	})

	T.Run("names itself in the drain failure", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "watcher", newWatcherRunner(newWatcher(t)).name)

		// A watcher's own Run returns as soon as its context is done, so the
		// budget is spent on a loop standing in for one that will not.
		loop := newBlockingLoop(30 * time.Second)
		runner := newLoopRunner("watcher", loop.run)

		go runner.Run()

		loop.awaitEntry(t)

		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		err := runner.Close(ctx)

		// Shutdown reports which of the two operations loops would not stop,
		// and the worker's name for the watcher's failure would send whoever
		// reads it to the wrong loop.
		must.Error(t, err)
		test.StrContains(t, err.Error(), "waiting for the operations watcher to drain")
	})

	T.Run("closes the watcher once however many times it is called", func(t *testing.T) {
		t.Parallel()

		var closes atomic.Int64

		runner := newLoopRunner("watcher", newBlockingLoop(0).run)
		runner.release = func() error {
			closes.Add(1)

			return nil
		}

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		must.NoError(t, runner.Close(ctx))
		must.NoError(t, runner.Close(ctx))

		test.EqOp(t, int64(1), closes.Load())
	})

	T.Run("reports what the release failed with, every time it is asked", func(t *testing.T) {
		t.Parallel()

		// Watcher.Close reports nil today, and Close's contract is that a
		// shutdown hears about everything that failed rather than the first
		// thing — so a release that does fail is reported rather than dropped
		// on the way to the drain.
		boom := platformerrors.New("closing the watcher")

		runner := newLoopRunner("watcher", newBlockingLoop(0).run)
		runner.release = func() error { return boom }

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		test.ErrorIs(t, runner.Close(ctx), boom)
		test.ErrorIs(t, runner.Close(ctx), boom)
	})
}
