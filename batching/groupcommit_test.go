package batching

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// row is the item type these tests batch: a key plus a value, so a merge has
// something to choose between.
type row struct {
	key   string
	value int
}

// rowKey and mergeRows are the pair a caller supplies to WithMerge. mergeRows
// keeps the larger value, which is arbitrary but observable.
func rowKey(r row) string { return r.key }

func mergeRows(existing, incoming row) row {
	return row{key: existing.key, value: max(existing.value, incoming.value)}
}

// recordingWriter stands in for the caller's write, capturing every flush so a
// test can see how the batcher merged and ordered what it was given.
type recordingWriter struct {
	// gate, when non-nil, holds each flush open until it is closed. That is how
	// a test makes callers arrive "while a flush is in flight", which is the
	// only condition under which merging happens at all.
	gate chan struct{}

	err error

	batches [][]row
	// deadlines records the deadline each flush's context carried, which is how
	// the "not the caller's context" contract is checked.
	deadlines []time.Time
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

func (w *recordingWriter) write(ctx context.Context, items []row) error {
	w.started.Add(1)

	if w.gate != nil {
		<-w.gate
	}

	w.flushes.Add(1)

	deadline, _ := ctx.Deadline()

	w.mu.Lock()
	defer w.mu.Unlock()

	w.batches = append(w.batches, items)
	w.deadlines = append(w.deadlines, deadline)

	return w.err
}

func (w *recordingWriter) rows() [][]row {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([][]row(nil), w.batches...)
}

func (w *recordingWriter) keys() []string {
	var keys []string

	for _, batch := range w.rows() {
		for _, r := range batch {
			keys = append(keys, r.key)
		}
	}

	return keys
}

// newTestGroupCommit builds a merging batcher and closes it afterwards.
func newTestGroupCommit(t *testing.T, w *recordingWriter, opts ...Option) *GroupCommit[row] {
	t.Helper()

	g, err := NewGroupCommit(w.write, append([]Option{WithMerge(rowKey, mergeRows)}, opts...)...)
	must.NoError(t, err)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
		defer cancel()

		_ = g.Close(ctx)
	})
	// Registered last, so it runs first: the gate has to open before the Close
	// above can finish, and a failed test never reaches its own close(gate).
	t.Cleanup(w.release)

	return g
}

func TestNewGroupCommit(T *testing.T) {
	T.Parallel()

	T.Run("a nil write function is refused", func(t *testing.T) {
		t.Parallel()

		g, err := NewGroupCommit[row](nil)

		test.Nil(t, g)
		test.ErrorIs(t, err, ErrNilWriteFunc)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	// Option cannot carry the item type, so a merge built for another one has to
	// be caught here or the batch would silently write in map order.
	T.Run("a merge for another item type is refused", func(t *testing.T) {
		t.Parallel()

		g, err := NewGroupCommit((&recordingWriter{}).write,
			WithMerge(func(s string) string { return s }, nil))

		test.Nil(t, g)
		test.ErrorIs(t, err, ErrItemTypeMismatch)
	})

	T.Run("an order for another item type is refused", func(t *testing.T) {
		t.Parallel()

		g, err := NewGroupCommit((&recordingWriter{}).write, WithOrder(strings.Compare))

		test.Nil(t, g)
		test.ErrorIs(t, err, ErrItemTypeMismatch)
	})
}

func TestGroupCommit_Submit(T *testing.T) {
	T.Parallel()

	T.Run("a lone caller's items are written", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		g := newTestGroupCommit(t, w)

		must.NoError(t, g.Submit(loose(t), row{key: "a", value: 3}))

		batches := w.rows()
		must.SliceLen(t, 1, batches)
		must.SliceLen(t, 1, batches[0])
		test.EqOp(t, "a", batches[0][0].key)
		test.EqOp(t, 3, batches[0][0].value)
	})

	T.Run("submitting nothing writes nothing", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		g := newTestGroupCommit(t, w)

		must.NoError(t, g.Submit(loose(t)))

		test.EqOp(t, int64(0), w.started.Load())
	})

	// The point of the whole type: callers arriving during a flush ride the next
	// one together, so a busy process issues one statement rather than N.
	T.Run("callers arriving during a flush are merged into one", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{gate: make(chan struct{})}
		g := newTestGroupCommit(t, w)

		// The first caller occupies the flusher; everyone after it accumulates.
		first := make(chan error, 1)
		submitCtx := loose(t)

		go func() { first <- g.Submit(submitCtx, row{key: "first"}) }()

		waitFor(t, func() bool { return w.started.Load() > 0 })

		var wg sync.WaitGroup

		results := make([]error, 3)

		for i, key := range []string{"a", "b", "c"} {
			wg.Go(func() { results[i] = g.Submit(submitCtx, row{key: key}) })
		}

		// Every merged caller has to be in the open batch before the gate opens,
		// or they would trickle into separate flushes and prove nothing.
		waitFor(t, func() bool { return g.Pending() == 3 })

		w.release()
		must.NoError(t, awaitErr(t, first))
		wg.Wait()

		for _, err := range results {
			test.NoError(t, err)
		}

		must.NoError(t, g.Close(loose(t)))

		batches := w.rows()
		must.SliceLen(t, 2, batches)
		test.SliceLen(t, 1, batches[0])
		test.SliceLen(t, 3, batches[1])
	})

	T.Run("duplicate keys collapse through the merge", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		g := newTestGroupCommit(t, w)

		must.NoError(t, g.Submit(loose(t),
			row{key: "a", value: 1},
			row{key: "a", value: 7},
			row{key: "a", value: 3},
		))

		batches := w.rows()
		must.SliceLen(t, 1, batches)
		must.SliceLen(t, 1, batches[0])
		test.EqOp(t, 7, batches[0][0].value)
	})

	// The lock ordering the whole design rests on: the write function sees one
	// total order, whatever order the callers arrived in.
	T.Run("a merged batch comes out in key order", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		g := newTestGroupCommit(t, w)

		must.NoError(t, g.Submit(loose(t), row{key: "c"}, row{key: "a"}, row{key: "b"}))

		batches := w.rows()
		must.SliceLen(t, 1, batches)
		must.SliceLen(t, 3, batches[0])
		test.Eq(t, []string{"a", "b", "c"}, w.keys())
	})

	// Without WithMerge the batcher invents no key, so nothing is collapsed and
	// arrival order is preserved.
	T.Run("without a merge every item is written in arrival order", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}

		g, err := NewGroupCommit(w.write)
		must.NoError(t, err)

		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
			defer cancel()

			_ = g.Close(ctx)
		})

		must.NoError(t, g.Submit(loose(t), row{key: "c"}, row{key: "a"}, row{key: "c"}))

		test.Eq(t, []string{"c", "a", "c"}, w.keys())
	})

	T.Run("a nil merge keeps the last item to arrive for a key", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}

		g, err := NewGroupCommit(w.write, WithMerge(rowKey, nil))
		must.NoError(t, err)

		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
			defer cancel()

			_ = g.Close(ctx)
		})

		must.NoError(t, g.Submit(loose(t), row{key: "a", value: 1}, row{key: "a", value: 2}))

		batches := w.rows()
		must.SliceLen(t, 1, batches)
		must.SliceLen(t, 1, batches[0])
		test.EqOp(t, 2, batches[0][0].value)
	})

	// Merge decides which items are written; order decides in what sequence.
	T.Run("WithOrder reorders a merged batch", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		g := newTestGroupCommit(t, w, WithOrder(func(a, b row) int { return b.value - a.value }))

		must.NoError(t, g.Submit(loose(t),
			row{key: "a", value: 1},
			row{key: "b", value: 9},
			row{key: "c", value: 5},
		))

		test.Eq(t, []string{"b", "c", "a"}, w.keys())
	})

	T.Run("a write failure reaches every waiter on that batch", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("write exploded")
		w := &recordingWriter{gate: make(chan struct{}), err: sentinel}
		g := newTestGroupCommit(t, w)

		first := make(chan error, 1)
		submitCtx := loose(t)

		go func() { first <- g.Submit(submitCtx, row{key: "first"}) }()

		waitFor(t, func() bool { return w.started.Load() > 0 })

		var wg sync.WaitGroup

		results := make([]error, 2)

		for i, key := range []string{"a", "b"} {
			wg.Go(func() { results[i] = g.Submit(submitCtx, row{key: key}) })
		}

		waitFor(t, func() bool { return g.Pending() == 2 })

		w.release()
		test.ErrorIs(t, awaitErr(t, first), sentinel)
		wg.Wait()

		for _, err := range results {
			test.ErrorIs(t, err, sentinel)
		}
	})

	// A caller that gives up must not cancel the flush the rest of the batch is
	// still waiting on.
	T.Run("a caller's expired context does not stop the flush", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{gate: make(chan struct{})}
		g := newTestGroupCommit(t, w)

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan error, 1)
		go func() { done <- g.Submit(ctx, row{key: "abandoned"}) }()

		waitFor(t, func() bool { return w.started.Load() > 0 })
		cancel()

		test.ErrorIs(t, awaitErr(t, done), context.Canceled)

		w.release()
		must.NoError(t, g.Close(loose(t)))

		// The items landed anyway, which is the right outcome: the work was
		// still worth doing, and its other waiters were still waiting for it.
		test.True(t, w.flushes.Load() > 0)
	})

	// The flush runs on a context of the batcher's own, bounded by the flush
	// timeout, so no waiter's deadline can shorten it.
	T.Run("the flush carries its own deadline, not the caller's", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		g := newTestGroupCommit(t, w, WithFlushTimeout(time.Hour))

		// The caller's deadline is the short one — testDeadline against an hour
		// — so what the write sees cannot have come from here. Short in the
		// other direction too: a waiter that never gets its batch waits this out
		// rather than the caller's whole patience.
		must.NoError(t, g.Submit(loose(t), row{key: "a"}))

		w.mu.Lock()
		defer w.mu.Unlock()

		must.SliceLen(t, 1, w.deadlines)
		test.True(t, time.Until(w.deadlines[0]) > 30*time.Minute)
	})
}

func TestGroupCommit_Pending(T *testing.T) {
	T.Parallel()

	T.Run("an idle batcher holds nothing", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, 0, newTestGroupCommit(t, &recordingWriter{}).Pending())
	})

	T.Run("merged duplicates count once", func(t *testing.T) {
		t.Parallel()

		g := newTestGroupCommit(t, &recordingWriter{})

		// The flusher is stopped so the open batch stays open long enough to be
		// counted; otherwise it is a race against a flush that is trying to take
		// it away.
		g.stopOnce.Do(func() { close(g.stop) })
		<-g.done

		g.join([]row{{key: "a"}, {key: "a"}, {key: "b"}})

		test.EqOp(t, 2, g.Pending())
	})
}

func TestGroupCommit_Close(T *testing.T) {
	T.Parallel()

	// The flusher is stopped first so that nothing but Close can write the open
	// batch, which is the whole claim being made.
	T.Run("close writes what was still accumulating", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		g := newTestGroupCommit(t, w)

		g.stopOnce.Do(func() { close(g.stop) })
		<-g.done

		g.join([]row{{key: "straggler"}})

		must.NoError(t, g.Close(loose(t)))

		test.Eq(t, []string{"straggler"}, w.keys())
	})

	T.Run("a failing final flush is reported", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("write exploded")
		w := &recordingWriter{err: sentinel}
		g := newTestGroupCommit(t, w)

		g.stopOnce.Do(func() { close(g.stop) })
		<-g.done

		g.join([]row{{key: "straggler"}})

		test.ErrorIs(t, g.Close(loose(t)), sentinel)
	})

	T.Run("a waiter parked on the final batch is released", func(t *testing.T) {
		t.Parallel()

		w := &recordingWriter{}
		g := newTestGroupCommit(t, w)

		g.stopOnce.Do(func() { close(g.stop) })
		<-g.done

		done := make(chan error, 1)
		submitCtx := loose(t)

		go func() { done <- g.Submit(submitCtx, row{key: "parked"}) }()

		waitFor(t, func() bool { return g.Pending() == 1 })

		must.NoError(t, g.Close(loose(t)))
		must.NoError(t, awaitErr(t, done))
	})

	T.Run("submitting after close is refused rather than parked", func(t *testing.T) {
		t.Parallel()

		g := newTestGroupCommit(t, &recordingWriter{})

		must.NoError(t, g.Close(loose(t)))

		test.ErrorIs(t, g.Submit(loose(t), row{key: "late"}), ErrClosed)
	})

	T.Run("close is safe to call more than once", func(t *testing.T) {
		t.Parallel()

		g := newTestGroupCommit(t, &recordingWriter{})

		must.NoError(t, g.Close(loose(t)))
		must.NoError(t, g.Close(loose(t)))
	})
}

// testDeadline is how long anything in this package's tests waits before it
// gives up and fails. waitFor already worked this way; loose gives the same
// bound to the waits that are not polls, and unbounded is the one thing none of
// them may be — every wait here is on work another goroutine has to finish, and
// a change that stops it happening should fail a case rather than park the
// whole test binary until `go test` gives up on it.
const testDeadline = 5 * time.Second

// loose returns a context bounded by testDeadline, for the calls these tests
// deliberately do not hand t.Context: a caller whose submission has to outlive
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
