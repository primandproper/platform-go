package workqueue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/batching"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingWriter stands in for the upsert, capturing every flush so a test can
// see how the batcher merged what it was given.
type recordingWriter struct {
	// gate, when non-nil, holds each flush open until it is closed. That is how
	// a test makes callers arrive "while a flush is in flight", which is the
	// only condition under which merging happens at all.
	gate chan struct{}

	err error

	batches [][]encodedEntry
	// started counts flushes that have begun, flushes those that have finished.
	// A test that wants a caller to arrive "during a flush" has to wait on the
	// former: the latter cannot tick until the gate opens.
	started atomic.Int64
	flushes atomic.Int64
	mu      sync.Mutex

	gateOnce sync.Once
}

// release opens the gate, once, whether or not the test got as far as opening
// it itself. A test that fails while a flush is held at the gate would
// otherwise leave that flush parked there forever, and Close waits for it: the
// failure would arrive as a hung test binary instead of as a failed case.
func (w *recordingWriter) release() {
	if w.gate == nil {
		return
	}

	w.gateOnce.Do(func() { close(w.gate) })
}

func (w *recordingWriter) write(_ context.Context, rows []encodedEntry) error {
	w.started.Add(1)

	if w.gate != nil {
		<-w.gate
	}

	w.flushes.Add(1)

	w.mu.Lock()
	defer w.mu.Unlock()

	w.batches = append(w.batches, rows)

	return w.err
}

func (w *recordingWriter) rows() [][]encodedEntry {
	w.mu.Lock()
	defer w.mu.Unlock()

	out := make([][]encodedEntry, len(w.batches))
	copy(out, w.batches)

	return out
}

// newTestBatcher builds the queue's batcher for a test and closes it afterwards.
// The tests below exercise the merging through this package's configuration of
// batching.GroupCommit rather than through a Queue, which would need a database
// to say anything about how rows were merged before they reached one.
func newTestBatcher(t *testing.T, w *recordingWriter) *batching.GroupCommit[encodedEntry] {
	t.Helper()

	b, err := newEnqueueBatcher(w.write)
	must.NoError(t, err)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
		defer cancel()

		_ = b.Close(ctx)
	})
	// Registered last, so it runs first: the gate has to open before the Close
	// above can finish, and a failed test never reaches its own close(gate).
	t.Cleanup(w.release)

	return b
}

func TestEnqueueBatcher(T *testing.T) {
	T.Parallel()

	T.Run("a lone caller's rows are written", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		b := newTestBatcher(t, w)

		must.NoError(t, b.Submit(loose(t), encodedEntry{key: "a", priority: 3}))

		batches := w.rows()
		must.SliceLen(t, 1, batches)
		must.SliceLen(t, 1, batches[0])
		test.EqOp(t, "a", batches[0][0].key)
		test.EqOp(t, 3, batches[0][0].priority)
	})

	// The point of the whole type: callers arriving during a flush ride the next
	// one together, so a busy process issues one statement rather than N.
	T.Run("callers arriving during a flush are merged into one", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{gate: make(chan struct{})}
		b := newTestBatcher(t, w)

		// The first caller occupies the flusher; everyone after it accumulates.
		submitCtx := loose(t)

		first := make(chan error, 1)
		go func() { first <- b.Submit(submitCtx, encodedEntry{key: "first"}) }()

		waitFor(t, func() bool { return w.started.Load() > 0 })

		var wg sync.WaitGroup
		results := make([]error, 3)

		for i, key := range []string{"a", "b", "c"} {
			wg.Go(func() {
				results[i] = b.Submit(submitCtx, encodedEntry{key: key})
			})
		}

		// Every merged caller has to be in the open batch before the gate opens,
		// or they would trickle into separate flushes and prove nothing.
		waitFor(t, func() bool { return b.Pending() == 3 })

		w.release()
		must.NoError(t, awaitErr(t, first))
		wg.Wait()

		for _, err := range results {
			test.NoError(t, err)
		}

		must.NoError(t, b.Close(loose(t)))

		batches := w.rows()
		must.SliceLen(t, 2, batches)
		test.SliceLen(t, 1, batches[0])
		test.SliceLen(t, 3, batches[1])
	})

	// The batch's merge rule has to match the ON CONFLICT clause exactly, or one
	// key's outcome would depend on whether two callers happened to land in the
	// same flush.
	T.Run("duplicate keys collapse to the loudest and soonest", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		b := newTestBatcher(t, w)

		must.NoError(t, b.Submit(loose(t),
			encodedEntry{key: "a", priority: 1, delayMicros: 500},
			encodedEntry{key: "a", priority: 7, delayMicros: 20},
			encodedEntry{key: "a", priority: 3, delayMicros: 900},
		))

		batches := w.rows()
		must.SliceLen(t, 1, batches)
		must.SliceLen(t, 1, batches[0])
		test.EqOp(t, 7, batches[0][0].priority)
		test.EqOp(t, int64(20), batches[0][0].delayMicros)
	})

	// buildUpsert's lock ordering depends on this, so it is the batcher's job
	// rather than a caller's.
	T.Run("a flushed batch is sorted by key", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		b := newTestBatcher(t, w)

		must.NoError(t, b.Submit(loose(t),
			encodedEntry{key: "c"}, encodedEntry{key: "a"}, encodedEntry{key: "b"},
		))

		batches := w.rows()
		must.SliceLen(t, 1, batches)
		must.SliceLen(t, 3, batches[0])
		test.EqOp(t, "a", batches[0][0].key)
		test.EqOp(t, "b", batches[0][1].key)
		test.EqOp(t, "c", batches[0][2].key)
	})

	T.Run("a write failure reaches every waiter on that batch", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("upsert exploded")
		w := &recordingWriter{err: sentinel}
		b := newTestBatcher(t, w)

		test.ErrorIs(t, b.Submit(loose(t), encodedEntry{key: "a"}), sentinel)
	})

	// A caller that gives up must not cancel the flush the rest of the batch is
	// still waiting on.
	T.Run("a caller's expired context does not stop the flush", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{gate: make(chan struct{})}
		b := newTestBatcher(t, w)

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan error, 1)
		go func() { done <- b.Submit(ctx, encodedEntry{key: "abandoned"}) }()

		waitFor(t, func() bool { return w.started.Load() > 0 })
		cancel()

		test.ErrorIs(t, awaitErr(t, done), context.Canceled)

		w.release()
		must.NoError(t, b.Close(loose(t)))

		// The keys landed anyway, which is the right outcome: the work was still
		// worth doing, and its other waiters were still waiting for it.
		test.True(t, w.flushes.Load() > 0)
	})

	// Whatever accumulated while the previous flush was in flight has to reach
	// the database, whether the flusher picks it up on its next pass or Close
	// writes it — a straggler dropped here is a unit of work nobody ever does.
	T.Run("nothing still accumulating is lost at close", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{gate: make(chan struct{})}
		b := newTestBatcher(t, w)

		submitCtx := loose(t)

		first := make(chan error, 1)
		go func() { first <- b.Submit(submitCtx, encodedEntry{key: "first"}) }()

		waitFor(t, func() bool { return w.started.Load() > 0 })

		straggler := make(chan error, 1)
		go func() { straggler <- b.Submit(submitCtx, encodedEntry{key: "straggler"}) }()

		waitFor(t, func() bool { return b.Pending() == 1 })

		closed := make(chan error, 1)
		go func() { closed <- b.Close(submitCtx) }()

		w.release()

		must.NoError(t, awaitErr(t, first))
		must.NoError(t, awaitErr(t, straggler))
		must.NoError(t, awaitErr(t, closed))

		var keys []string
		for _, batch := range w.rows() {
			for _, row := range batch {
				keys = append(keys, row.key)
			}
		}

		test.SliceContains(t, keys, "straggler")
	})

	T.Run("enqueue after close is refused rather than parked", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		b := newTestBatcher(t, w)

		must.NoError(t, b.Close(loose(t)))

		test.ErrorIs(t, b.Submit(loose(t), encodedEntry{key: "late"}), batching.ErrClosed)
	})

	// Enqueue restates the batcher's refusal as this package's ErrClosed, which
	// is only sound if the two are the same sentinel to errors.Is.
	T.Run("ErrClosed wraps the batcher's sentinel", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ErrClosed, batching.ErrClosed)
	})

	T.Run("close is safe to call more than once", func(t *testing.T) {
		t.Parallel()

		b := newTestBatcher(t, &recordingWriter{})

		must.NoError(t, b.Close(loose(t)))
		must.NoError(t, b.Close(loose(t)))
	})
}

func TestMergeEntries(T *testing.T) {
	T.Parallel()

	T.Run("priority rises and delay falls", func(t *testing.T) {
		t.Parallel()

		merged := mergeEntries(
			encodedEntry{key: "a", priority: 5, delayMicros: 100},
			encodedEntry{key: "a", priority: 1, delayMicros: 10},
		)

		test.EqOp(t, 5, merged.priority)
		test.EqOp(t, int64(10), merged.delayMicros)
	})
}

func TestSortAndDedupe(T *testing.T) {
	T.Parallel()

	T.Run("sorts and removes repeats", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{"a", "b", "c"}, sortAndDedupe([]string{"c", "a", "b", "a", "c"}))
	})

	T.Run("an empty batch stays empty", func(t *testing.T) {
		t.Parallel()

		test.SliceEmpty(t, sortAndDedupe(nil))
	})
}

// testDeadline is how long anything in this file waits before it gives up and
// fails. waitFor already worked this way; loose and awaitErr give the same
// bound to the waits that are not polls, and unbounded is the one thing none of
// them may be — every wait here is on a flush another goroutine has to finish,
// and a change that stops it happening should fail a case rather than park the
// whole test binary until `go test` gives up on it.
const testDeadline = 5 * time.Second

// loose returns a context bounded by testDeadline, for the submissions these
// tests deliberately do not hand t.Context: a caller whose work has to outlive
// the assertion that started it. Not the test's context still has to mean a
// context.
func loose(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
	t.Cleanup(cancel)

	return ctx
}

// awaitErr receives one error, failing the test rather than blocking if the
// goroutine that owes it never gets there.
func awaitErr(t *testing.T, ch <-chan error) error {
	t.Helper()

	select {
	case err := <-ch:
		return err
	case <-time.After(testDeadline):
		t.Fatal("nothing arrived on the channel before the deadline")

		return nil
	}
}

// waitFor polls until cond holds, failing the test rather than hanging if it
// never does.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(testDeadline)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatal("condition never held")
}
