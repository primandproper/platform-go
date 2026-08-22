package batching

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
)

// Buffer coalesces keys in memory and writes them on an interval or when it
// fills. Callers never block and never learn whether the write succeeded.
//
// It is GroupCommit with the guarantee removed, and that is the whole choice
// between them: use a Buffer where the write is a side effect of serving a
// request — a last-seen timestamp, an access counter, a freshness marker — and
// nothing reads it back in the same breath. Ten thousand requests naming the
// same hundred keys become one statement per interval, and no request waits for
// it.
//
// A Buffer owns a goroutine and must be Closed.
type Buffer[K comparable] struct {
	// write is what a flush actually runs, holding no dependency on where the
	// keys go.
	write func(ctx context.Context, keys []K) error

	order func(a, b K) int

	clock clock.Clock
	o11y  observability.Observer

	// pending is what the next flush will write. flushing is what the current
	// one is writing, kept visible so Take can tell "not buffered" from
	// "buffered and already going out"; it is nil the rest of the time.
	pending  map[K]struct{}
	flushing map[K]struct{}

	// flushDone closes when the in-flight flush finishes, and is nil when
	// nothing is in flight. It is what Take waits on.
	flushDone chan struct{}

	wake chan struct{}
	stop chan struct{}
	done chan struct{}

	flushes        *metrics.OperationSet
	sizes          metrics.Float64Histogram
	droppedCounter metrics.Int64Counter

	dropped atomic.Uint64

	flushInterval time.Duration
	flushTimeout  time.Duration
	maxPending    int

	mu       sync.Mutex
	closed   bool
	stopOnce sync.Once
}

// NewBuffer starts a buffer that flushes through write, and begins its flusher
// goroutine.
//
// A flush failure reaches no caller, because by then there is no caller: the
// request that added the key was answered long ago. It is logged through
// WithLogger and counted on the batching_buffer_errors counter, and the keys are
// dropped rather than held for another attempt — retrying is a policy, and a
// policy that lives inside a buffer nobody is watching is how a write path grows
// an unbounded queue in memory. A caller that cannot afford to lose the write
// wants GroupCommit, or a queue.
func NewBuffer[K comparable](write func(ctx context.Context, keys []K) error, opts ...Option) (*Buffer[K], error) {
	if write == nil {
		return nil, ErrNilWriteFunc
	}

	o := newOptions(opts)

	shape, err := resolveOrdering[K](o)
	if err != nil {
		return nil, err
	}

	b := &Buffer[K]{
		write:         write,
		order:         shape.order,
		clock:         o.clock,
		o11y:          observability.NewObserver(bufferName, o.logger, o.tracerProvider),
		pending:       make(map[K]struct{}),
		flushInterval: o.flushInterval,
		flushTimeout:  o.flushTimeout,
		maxPending:    o.maxPending,
		wake:          make(chan struct{}, 1),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}

	if b.flushes, b.sizes, err = buildInstruments(o.metricsProvider, bufferName); err != nil {
		return nil, err
	}

	if b.droppedCounter, err = metrics.EnsureMetricsProvider(o.metricsProvider).
		NewInt64Counter(bufferName + "_dropped"); err != nil {
		return nil, platformerrors.Wrapf(err, "creating %s dropped counter", bufferName)
	}

	go b.run()

	return b, nil
}

// Add records keys for a later flush. It never blocks, and repeats of a key
// already pending cost nothing — that collapse is the point, not an
// optimization on top of it.
//
// Keys added after Close are dropped and counted; see Dropped. That is the one
// way a key can go missing without a failing write, and it means a caller kept
// serving requests through a component it had already shut down.
func (b *Buffer[K]) Add(keys ...K) {
	if len(keys) == 0 {
		return
	}

	b.mu.Lock()

	if b.closed {
		b.mu.Unlock()

		b.dropped.Add(uint64(len(keys)))
		b.droppedCounter.Add(context.Background(), int64(len(keys)))

		return
	}

	for i := range keys {
		b.pending[keys[i]] = struct{}{}
	}

	full := len(b.pending) >= b.maxPending

	b.mu.Unlock()

	if full {
		b.nudge()
	}
}

// Take removes keys from the buffer and returns those it was holding, ordered
// by WithOrder when one was supplied.
//
// It is how a caller expresses an ordering dependency: a foreground write that
// touches the same rows takes its keys back first, so the buffered write cannot
// land between the caller's read and its write. What comes back is what the
// buffer would have written and now will not — a key it never held is simply
// absent, which is not an error.
//
// If a flush is already carrying any of those keys, Take waits for it rather
// than returning while that write is still in flight; the wait is bounded by
// WithFlushTimeout, and ctx cancels it. On cancellation nothing is taken, so a
// caller that gets an error here has lost neither its keys nor the buffer's.
func (b *Buffer[K]) Take(ctx context.Context, keys ...K) ([]K, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	for {
		b.mu.Lock()

		if !b.flushingAny(keys) {
			taken := b.takeLocked(keys)
			b.mu.Unlock()

			return taken, nil
		}

		wait := b.flushDone

		b.mu.Unlock()

		select {
		case <-wait:
		case <-ctx.Done():
			return nil, platformerrors.Wrap(ctx.Err(), "waiting out an in-flight buffer flush")
		}
	}
}

// Pending reports how many distinct keys are waiting to be written. It is a
// sampled level and not a synchronization point: the flusher may take them the
// instant it returns.
func (b *Buffer[K]) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return len(b.pending)
}

// Dropped reports how many keys have been discarded because they were added
// after Close.
func (b *Buffer[K]) Dropped() uint64 {
	return b.dropped.Load()
}

// Close stops the flusher and writes whatever it still holds, on ctx rather
// than on a flush timeout of its own — shutdown has a deadline the caller owns.
// Safe to call more than once; Add afterwards drops.
func (b *Buffer[K]) Close(ctx context.Context) error {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	b.stopOnce.Do(func() { close(b.stop) })

	<-b.done // an in-flight flush finishes before run returns

	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()

	if err := b.flushPending(ctx); err != nil {
		return op.Error(err, "flushing buffered keys before close")
	}

	return nil
}

// nudge asks for a flush without waiting for one to be scheduled. The channel
// holds one token, so a burst of full-buffer Adds costs one extra flush rather
// than one per Add.
func (b *Buffer[K]) nudge() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// run flushes on the interval, and early whenever the buffer fills, until
// close.
func (b *Buffer[K]) run() {
	defer close(b.done)

	ticker := b.clock.NewTicker(b.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-b.stop:
			return
		case <-ticker.Chan():
		case <-b.wake:
		}

		b.flushLoose()
	}
}

// flushLoose runs one flush on a context of its own. The caller who added these
// keys is long gone, so there is no context to inherit and no cancellation that
// would mean anything — and no caller to hand the error to either, which is the
// contract this type is named for. flushPending has already logged and counted
// it.
func (b *Buffer[K]) flushLoose() {
	ctx, cancel := context.WithTimeout(context.Background(), b.flushTimeout)
	defer cancel()

	if err := b.flushPending(ctx); err != nil {
		// Already logged and counted by flushPending, and there is nobody here
		// to hand it to — which is the contract this type is named for.
		return
	}
}

// flushPending writes whatever has accumulated, if anything has.
func (b *Buffer[K]) flushPending(ctx context.Context) error {
	keys, done := b.takeForFlush()
	if len(keys) == 0 {
		return nil
	}

	defer b.finishFlush(done)

	ctx, op := b.o11y.BeginCustom(ctx, bufferName+".flush")
	defer op.End()

	op.Set(itemCountKey, len(keys))

	b.flushes.Attempt(ctx)
	b.sizes.Record(ctx, float64(len(keys)))

	defer op.Time(ctx, b.clock, b.flushes.Latency)()

	if err := b.write(ctx, keys); err != nil {
		b.flushes.Failed(ctx)

		return op.Error(err, "writing buffered keys")
	}

	return nil
}

// takeForFlush swaps the pending set out and publishes it as in flight, so a
// concurrent Take can tell that those keys are already going out.
func (b *Buffer[K]) takeForFlush() (keys []K, done chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.pending) == 0 {
		return nil, nil
	}

	keys = slices.Collect(maps.Keys(b.pending))
	if b.order != nil {
		slices.SortFunc(keys, b.order)
	}

	b.flushing = b.pending
	b.pending = make(map[K]struct{}, len(keys))
	b.flushDone = make(chan struct{})

	return keys, b.flushDone
}

// finishFlush retires the in-flight set and releases whoever was waiting on it.
func (b *Buffer[K]) finishFlush(done chan struct{}) {
	b.mu.Lock()
	b.flushing = nil
	b.flushDone = nil
	b.mu.Unlock()

	close(done)
}

// flushingAny reports whether the in-flight flush carries any of keys. Called
// under mu.
func (b *Buffer[K]) flushingAny(keys []K) bool {
	if len(b.flushing) == 0 {
		return false
	}

	for i := range keys {
		if _, ok := b.flushing[keys[i]]; ok {
			return true
		}
	}

	return false
}

// takeLocked removes keys from the pending set and returns the ones that were
// there, in the buffer's order. Called under mu.
func (b *Buffer[K]) takeLocked(keys []K) []K {
	taken := make([]K, 0, len(keys))

	for i := range keys {
		if _, ok := b.pending[keys[i]]; ok {
			delete(b.pending, keys[i])
			taken = append(taken, keys[i])
		}
	}

	if len(taken) == 0 {
		return nil
	}

	if b.order != nil {
		slices.SortFunc(taken, b.order)
	}

	return taken
}
