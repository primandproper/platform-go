package operations

import (
	"context"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newTestWatcher builds a watcher with a fast poll and a wakeup channel the test
// drives, so nothing here waits on a real interval.
func newTestWatcher(t *testing.T, store Store, wakeup <-chan struct{}) *Watcher {
	t.Helper()

	// The floor the config enforces, which is also the shortest poll a test can
	// ask for. Nothing here waits on it: every test that needs a re-read drives
	// sweep directly or sends a wake.
	cfg := &WatcherConfig{
		Poll:            100 * time.Millisecond,
		MinReadInterval: time.Millisecond,
	}

	w, err := NewWatcher(t.Context(), cfg, store, WithWatcherWakeup(wakeup))
	must.NoError(t, err)

	t.Cleanup(func() { _ = w.Close() })

	return w
}

// receive waits for one snapshot, failing the test rather than hanging forever.
func receive(t *testing.T, ch <-chan *Operation) (*Operation, bool) {
	t.Helper()

	select {
	case op, ok := <-ch:
		return op, ok
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an operation snapshot")

		return nil, false
	}
}

func TestNewWatcher(T *testing.T) {
	T.Parallel()

	T.Run("rejects what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := NewWatcher(t.Context(), nil, newFakeStore())
		test.ErrorIs(t, err, ErrNilConfig)

		_, err = NewWatcher(t.Context(), &WatcherConfig{}, nil)
		test.ErrorIs(t, err, ErrNilStore)
	})

	T.Run("defaults an empty config", func(t *testing.T) {
		t.Parallel()

		w, err := NewWatcher(t.Context(), &WatcherConfig{}, newFakeStore())
		must.NoError(t, err)
		t.Cleanup(func() { _ = w.Close() })
	})
}

func TestWatcher_Watch(T *testing.T) {
	T.Parallel()

	// A caller that subscribes and is told nothing until something changes
	// cannot render a status page. The current state has to arrive first.
	T.Run("delivers the current state immediately", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", State: StateRunning, Revision: 4})
		w := newTestWatcher(t, store, nil)

		snapshots, err := w.Watch(t.Context(), "op1")
		must.NoError(t, err)

		op, ok := receive(t, snapshots)
		must.True(t, ok)
		test.EqOp(t, "op1", op.ID)
		test.EqOp(t, StateRunning, op.State)
	})

	// The case that would otherwise hang on exactly the requests that are
	// easiest to serve: an operation that finished before anybody subscribed.
	T.Run("a finished operation delivers its outcome and closes", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", State: StateSucceeded, Done: true, Revision: 9})
		w := newTestWatcher(t, store, nil)

		snapshots, err := w.Watch(t.Context(), "op1")
		must.NoError(t, err)

		op, ok := receive(t, snapshots)
		must.True(t, ok)
		test.True(t, op.Done)

		_, ok = receive(t, snapshots)
		test.False(t, ok)
	})

	T.Run("an unknown operation is an error, not an empty stream", func(t *testing.T) {
		t.Parallel()

		w := newTestWatcher(t, newFakeStore(), nil)

		_, err := w.Watch(t.Context(), "nope")

		test.ErrorIs(t, err, ErrOperationNotFound)
	})

	// Every subscription is a row in the re-read a wake triggers, so an
	// unbounded subscriber count turns one notification into an unbounded query.
	T.Run("refuses past the subscription limit", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", State: StateRunning})

		w, err := NewWatcher(t.Context(), &WatcherConfig{MaxSubscriptions: 1}, store)
		must.NoError(t, err)
		t.Cleanup(func() { _ = w.Close() })

		_, err = w.Watch(t.Context(), "op1")
		must.NoError(t, err)

		_, err = w.Watch(t.Context(), "op1")
		test.ErrorIs(t, err, ErrTooManyWatchers)
	})

	T.Run("a closed watcher refuses new subscriptions", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", State: StateRunning})
		w := newTestWatcher(t, store, nil)

		must.NoError(t, w.Close())
		must.NoError(t, w.Close()) // idempotent

		_, err := w.Watch(t.Context(), "op1")
		test.ErrorIs(t, err, ErrWatcherClosed)
	})
}

func TestWatcher_Run(T *testing.T) {
	T.Parallel()

	T.Run("delivers changes and closes on the terminal snapshot", func(t *testing.T) {
		t.Parallel()

		op := &Operation{ID: "op1", State: StateRunning, Revision: 1}
		store := newFakeStore(op)

		wakeup := make(chan struct{}, 1)
		w := newTestWatcher(t, store, wakeup)

		go func() { _ = w.Run(t.Context()) }()

		snapshots, err := w.Watch(t.Context(), "op1")
		must.NoError(t, err)

		first, ok := receive(t, snapshots)
		must.True(t, ok)
		test.EqOp(t, StateRunning, first.State)

		running := *op
		running.Progress = Progress{Count: 4300, UnitsDone: 3}
		store.put(&running)

		wakeup <- struct{}{}

		progressed, ok := receive(t, snapshots)
		must.True(t, ok)
		test.EqOp(t, int64(4300), progressed.Progress.Count)

		finished := running
		finished.State = StateSucceeded
		store.put(&finished)

		wakeup <- struct{}{}

		terminal, ok := receive(t, snapshots)
		must.True(t, ok)
		test.EqOp(t, StateSucceeded, terminal.State)
		test.True(t, terminal.Done)

		// The stream ends after the terminal snapshot, which is what lets a
		// consumer's loop exit without an "am I finished" check.
		_, ok = receive(t, snapshots)
		test.False(t, ok)
	})

	// The property the whole watch design rests on: what arrives is the whole
	// operation, so a subscriber that fell behind loses nothing by having the
	// intermediate value dropped.
	T.Run("a slow subscriber gets the latest, not the oldest", func(t *testing.T) {
		t.Parallel()

		op := &Operation{ID: "op1", State: StateRunning, Revision: 1}
		store := newFakeStore(op)

		wakeup := make(chan struct{}, 1)
		w := newTestWatcher(t, store, wakeup)

		snapshots, err := w.Watch(t.Context(), "op1")
		must.NoError(t, err)

		// Deliberately not drained: the first snapshot is sitting in the buffer
		// while four more arrive behind it.
		for count := range int64(5) {
			next := store.snapshot("op1")
			next.Progress.Count = (count + 1) * 1000
			store.put(next)

			w.sweep(t.Context())
		}

		latest, ok := receive(t, snapshots)
		must.True(t, ok)
		test.EqOp(t, int64(5000), latest.Progress.Count)
	})

	T.Run("an unchanged row delivers nothing", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", State: StateRunning, Revision: 3})
		w := newTestWatcher(t, store, nil)

		snapshots, err := w.Watch(t.Context(), "op1")
		must.NoError(t, err)

		_, ok := receive(t, snapshots)
		must.True(t, ok)

		w.sweep(t.Context())
		w.sweep(t.Context())

		select {
		case op := <-snapshots:
			t.Fatalf("a re-read of an unchanged row delivered %v", op)
		case <-time.After(50 * time.Millisecond):
		}
	})

	// One failed read must not stop delivery to every subscriber: the next tick
	// is a couple of seconds away and the operation is still running.
	T.Run("a failed re-read is survivable", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", State: StateRunning, Revision: 1})
		w := newTestWatcher(t, store, nil)

		snapshots, err := w.Watch(t.Context(), "op1")
		must.NoError(t, err)

		_, ok := receive(t, snapshots)
		must.True(t, ok)

		store.mu.Lock()
		store.getManyErr = platformerrors.New("the database is having a moment")
		store.mu.Unlock()

		w.sweep(t.Context())

		store.mu.Lock()
		store.getManyErr = nil
		store.mu.Unlock()

		finished := &Operation{ID: "op1", State: StateSucceeded, Revision: 1}
		store.put(finished)

		w.sweep(t.Context())

		terminal, ok := receive(t, snapshots)
		must.True(t, ok)
		test.EqOp(t, StateSucceeded, terminal.State)
	})

	T.Run("a cancelled context retires the subscription", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", State: StateRunning, Revision: 1})
		w := newTestWatcher(t, store, nil)

		ctx, cancel := context.WithCancel(t.Context())

		snapshots, err := w.Watch(ctx, "op1")
		must.NoError(t, err)

		_, ok := receive(t, snapshots)
		must.True(t, ok)

		cancel()

		_, ok = receive(t, snapshots)
		test.False(t, ok)

		test.EqOp(t, 0, w.total())
	})

	T.Run("closing the watcher retires every subscription", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", State: StateRunning, Revision: 1})
		w := newTestWatcher(t, store, nil)

		snapshots, err := w.Watch(t.Context(), "op1")
		must.NoError(t, err)

		_, ok := receive(t, snapshots)
		must.True(t, ok)

		must.NoError(t, w.Close())

		_, ok = receive(t, snapshots)
		test.False(t, ok)
	})

	// Two subscribers on one operation cost one query, which is the property
	// that lets a watcher be shared by a whole process.
	T.Run("several subscribers to one operation share the read", func(t *testing.T) {
		t.Parallel()

		op := &Operation{ID: "op1", State: StateRunning, Revision: 1}
		store := newFakeStore(op)
		w := newTestWatcher(t, store, nil)

		first, err := w.Watch(t.Context(), "op1")
		must.NoError(t, err)

		second, err := w.Watch(t.Context(), "op1")
		must.NoError(t, err)

		_, _ = receive(t, first)
		_, _ = receive(t, second)

		test.EqOp(t, 2, w.total())
		test.SliceLen(t, 1, w.watchedIDs())

		finished := *op
		finished.State = StateFailed
		store.put(&finished)

		w.sweep(t.Context())

		firstTerminal, ok := receive(t, first)
		must.True(t, ok)
		test.EqOp(t, StateFailed, firstTerminal.State)

		secondTerminal, ok := receive(t, second)
		must.True(t, ok)
		test.EqOp(t, StateFailed, secondTerminal.State)
	})

	T.Run("Run returns when the watcher is closed", func(t *testing.T) {
		t.Parallel()

		w := newTestWatcher(t, newFakeStore(), nil)

		done := make(chan error, 1)
		go func() { done <- w.Run(t.Context()) }()

		must.NoError(t, w.Close())

		select {
		case err := <-done:
			test.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not return after Close")
		}
	})

	T.Run("Run returns when its context is done", func(t *testing.T) {
		t.Parallel()

		w := newTestWatcher(t, newFakeStore(), nil)

		ctx, cancel := context.WithCancel(t.Context())

		done := make(chan error, 1)
		go func() { done <- w.Run(ctx) }()

		cancel()

		select {
		case err := <-done:
			test.Error(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not return after its context was cancelled")
		}
	})
}
