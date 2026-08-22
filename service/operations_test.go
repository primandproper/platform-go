package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/operations"

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
		runner := newLoopRunner(loop.run)

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
		runner := newLoopRunner(loop.run)

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
		runner := newLoopRunner(loop.run)

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
		runner := newLoopRunner(loop.run)

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
		runner := newLoopRunner(loop.run)

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
