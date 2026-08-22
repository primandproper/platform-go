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

// testFlushInterval is short enough that an interval-driven test finishes
// quickly and long enough that it never fires during a test asserting on what
// is still pending.
const testFlushInterval = 5 * time.Millisecond

// recordingFlusher stands in for the caller's write, capturing every flush.
type recordingFlusher struct {
	// gate, when non-nil, holds each flush open until it is closed, which is how
	// a test arranges for keys to be "already going out".
	gate chan struct{}

	err error

	batches [][]string
	started atomic.Int64
	mu      sync.Mutex

	gateOnce sync.Once
}

// release opens the gate, once, whether or not the test got as far as opening
// it itself. A test that fails while a flush is held at the gate would
// otherwise leave that flush parked there forever, and Close waits for it: the
// failure would arrive as a hung test binary instead of as a failed case.
func (f *recordingFlusher) release() {
	if f.gate == nil {
		return
	}

	f.gateOnce.Do(func() { close(f.gate) })
}

func (f *recordingFlusher) write(_ context.Context, keys []string) error {
	f.started.Add(1)

	if f.gate != nil {
		<-f.gate
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.batches = append(f.batches, keys)

	return f.err
}

func (f *recordingFlusher) rows() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([][]string(nil), f.batches...)
}

func (f *recordingFlusher) keys() []string {
	var keys []string

	for _, batch := range f.rows() {
		keys = append(keys, batch...)
	}

	return keys
}

// newTestBuffer builds a key-ordered buffer and closes it afterwards. The
// ordering is on because every assertion below about what was written would
// otherwise be reading a map's iteration order.
func newTestBuffer(t *testing.T, f *recordingFlusher, opts ...Option) *Buffer[string] {
	t.Helper()

	b, err := NewBuffer(f.write, append([]Option{
		WithOrder(strings.Compare),
		WithFlushInterval(testFlushInterval),
	}, opts...)...)
	must.NoError(t, err)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
		defer cancel()

		_ = b.Close(ctx)
	})
	// Registered last, so it runs first: the gate has to open before the Close
	// above can finish, and a failed test never reaches its own close(gate).
	t.Cleanup(f.release)

	return b
}

func TestNewBuffer(T *testing.T) {
	T.Parallel()

	T.Run("a nil write function is refused", func(t *testing.T) {
		t.Parallel()

		b, err := NewBuffer[string](nil)

		test.Nil(t, b)
		test.ErrorIs(t, err, ErrNilWriteFunc)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("an order for another key type is refused", func(t *testing.T) {
		t.Parallel()

		b, err := NewBuffer((&recordingFlusher{}).write, WithOrder(func(a, b int) int { return a - b }))

		test.Nil(t, b)
		test.ErrorIs(t, err, ErrItemTypeMismatch)
	})
}

func TestBuffer_Add(T *testing.T) {
	T.Parallel()

	T.Run("keys accumulate and flush on the interval", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{}
		b := newTestBuffer(t, f)

		b.Add("b", "a")

		waitFor(t, func() bool { return len(f.rows()) > 0 })

		test.Eq(t, []string{"a", "b"}, f.keys())
	})

	// The collapse is the point: ten thousand requests naming one key cost one
	// row, not ten thousand.
	T.Run("repeats of a pending key collapse", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{}
		b := newTestBuffer(t, f)

		for range 100 {
			b.Add("a")
		}

		waitFor(t, func() bool { return len(f.rows()) > 0 })

		test.Eq(t, []string{"a"}, f.rows()[0])
	})

	T.Run("adding nothing does not schedule a flush", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{}
		b := newTestBuffer(t, f, WithFlushInterval(time.Hour))

		b.Add()

		test.EqOp(t, 0, b.Pending())
		test.EqOp(t, int64(0), f.started.Load())
	})

	// A buffer that only ever flushed on its interval would hold an unbounded
	// batch under a burst.
	T.Run("a full buffer flushes without waiting for the interval", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{}
		b := newTestBuffer(t, f, WithFlushInterval(time.Hour), WithMaxPending(3))

		b.Add("a", "b", "c")

		waitFor(t, func() bool { return len(f.rows()) > 0 })

		test.Eq(t, []string{"a", "b", "c"}, f.rows()[0])
	})

	T.Run("keys added after close are dropped and counted", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{}
		b := newTestBuffer(t, f)

		must.NoError(t, b.Close(loose(t)))

		b.Add("a", "b")

		test.EqOp(t, uint64(2), b.Dropped())
		test.EqOp(t, 0, b.Pending())
		test.SliceEmpty(t, f.keys())
	})
}

func TestBuffer_Take(T *testing.T) {
	T.Parallel()

	T.Run("pending keys come back and stop being pending", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{}
		b := newTestBuffer(t, f, WithFlushInterval(time.Hour))

		b.Add("c", "a", "b")

		taken, err := b.Take(loose(t), "c", "a")
		must.NoError(t, err)

		test.Eq(t, []string{"a", "c"}, taken)
		test.EqOp(t, 1, b.Pending())

		must.NoError(t, b.Close(loose(t)))

		// Only what was left behind is written; the taken keys are the caller's
		// to write now, which is the whole point of taking them.
		test.Eq(t, []string{"b"}, f.keys())
	})

	T.Run("keys the buffer never held are absent rather than an error", func(t *testing.T) {
		t.Parallel()

		b := newTestBuffer(t, &recordingFlusher{}, WithFlushInterval(time.Hour))

		taken, err := b.Take(loose(t), "nothing")

		must.NoError(t, err)
		test.SliceEmpty(t, taken)
	})

	T.Run("taking nothing is a no-op", func(t *testing.T) {
		t.Parallel()

		b := newTestBuffer(t, &recordingFlusher{}, WithFlushInterval(time.Hour))

		taken, err := b.Take(loose(t))

		must.NoError(t, err)
		test.SliceEmpty(t, taken)
	})

	// Take exists to express an ordering dependency, so returning while a flush
	// is still writing those very keys would be the one thing it must not do.
	T.Run("a flush already carrying the keys is waited out", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{gate: make(chan struct{})}
		b := newTestBuffer(t, f)

		b.Add("a")

		waitFor(t, func() bool { return f.started.Load() > 0 })

		returned := make(chan struct{})

		go func() {
			defer close(returned)

			_, _ = b.Take(context.Background(), "a")
		}()

		select {
		case <-returned:
			t.Fatal("Take returned while the flush carrying its key was still in flight")
		case <-time.After(20 * time.Millisecond):
		}

		f.release()

		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Fatal("Take did not return once the flush carrying its key had finished")
		}
	})

	// A flush that touches none of the caller's keys is nothing to wait for.
	T.Run("a flush carrying other keys is not waited for", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{gate: make(chan struct{})}
		b := newTestBuffer(t, f)

		b.Add("a")

		waitFor(t, func() bool { return f.started.Load() > 0 })

		b.Add("b")

		taken, err := b.Take(loose(t), "b")

		must.NoError(t, err)
		test.Eq(t, []string{"b"}, taken)
	})

	T.Run("a cancelled wait takes nothing", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{gate: make(chan struct{})}
		b := newTestBuffer(t, f)

		b.Add("a")

		waitFor(t, func() bool { return f.started.Load() > 0 })

		// Re-added while the first flush holds it, so the key is both pending
		// and in flight: without the wait, Take would hand it back mid-write.
		b.Add("a")

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		taken, err := b.Take(ctx, "a")

		test.ErrorIs(t, err, context.Canceled)
		test.SliceEmpty(t, taken)
		test.EqOp(t, 1, b.Pending())
	})
}

func TestBuffer_Close(T *testing.T) {
	T.Parallel()

	T.Run("close writes what was still pending", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{}
		b := newTestBuffer(t, f, WithFlushInterval(time.Hour))

		b.Add("a", "b")

		must.NoError(t, b.Close(loose(t)))

		test.Eq(t, []string{"a", "b"}, f.keys())
	})

	T.Run("a failing final flush is reported", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("write exploded")
		f := &recordingFlusher{err: sentinel}
		b := newTestBuffer(t, f, WithFlushInterval(time.Hour))

		b.Add("a")

		test.ErrorIs(t, b.Close(loose(t)), sentinel)
	})

	T.Run("closing an empty buffer writes nothing", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{}
		b := newTestBuffer(t, f, WithFlushInterval(time.Hour))

		must.NoError(t, b.Close(loose(t)))

		test.EqOp(t, int64(0), f.started.Load())
	})

	T.Run("close is safe to call more than once", func(t *testing.T) {
		t.Parallel()

		b := newTestBuffer(t, &recordingFlusher{}, WithFlushInterval(time.Hour))

		must.NoError(t, b.Close(loose(t)))
		must.NoError(t, b.Close(loose(t)))
	})
}

// A flush failure reaches no caller by design — there is no caller left — so
// what it must not do is stall the buffer or hold the keys forever.
func TestBuffer_flushFailure(T *testing.T) {
	T.Parallel()

	T.Run("a failed flush does not hold its keys back", func(t *testing.T) {
		t.Parallel()

		f := &recordingFlusher{err: platformerrors.New("write exploded")}
		b := newTestBuffer(t, f)

		b.Add("a")

		waitFor(t, func() bool { return len(f.rows()) > 0 })

		test.EqOp(t, 0, b.Pending())

		b.Add("b")

		waitFor(t, func() bool { return len(f.rows()) > 1 })
	})
}
