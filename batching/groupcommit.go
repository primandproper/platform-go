package batching

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
)

// GroupCommit merges concurrent Submit calls into one write — group commit, the
// same trick a write-ahead log plays on fsync, for the same reason.
//
// It is safe for concurrent use and is meant to be shared: one per process per
// thing being written. A GroupCommit per caller would merge nothing, which is
// the only way to hold this type and get no benefit from it.
//
// A GroupCommit owns a goroutine and must be Closed.
type GroupCommit[T any] struct {
	// write is what a flush actually runs. Held as a function rather than a
	// storage handle so the merging can be exercised without one — it has no
	// other dependency on where the items go.
	write func(ctx context.Context, items []T) error

	newAccumulator func() accumulator[T]
	order          func(a, b T) int

	clock clock.Clock
	o11y  observability.Observer

	// open is the batch now accepting items; the flusher swaps it out under mu,
	// so a caller that captured it is guaranteed its items ride that flush.
	open *groupBatch[T]

	wake chan struct{}
	stop chan struct{}
	done chan struct{}

	flushes *metrics.OperationSet
	sizes   metrics.Float64Histogram

	flushTimeout time.Duration

	mu       sync.Mutex
	closed   bool
	stopOnce sync.Once
}

// groupBatch is one merged group of items plus the result its waiters read.
// Waiters read err only after done closes, which the flusher does last.
type groupBatch[T any] struct {
	items accumulator[T]
	done  chan struct{}
	err   error
}

// NewGroupCommit starts a batcher that flushes through write, and begins its
// flusher goroutine.
//
// The batcher is deliberately not timer-driven. A flush starts as soon as the
// previous one finishes, so an idle process pays no latency at all and a busy
// one merges more the busier it gets. There is no interval to tune and no
// configuration that can make it wrong — which is why WithFlushInterval is a
// Buffer option and not one of these.
//
// Pass WithMerge when several submissions can name the same row. Without it
// every submitted item is written, in arrival order, and the batcher is buying
// one statement per flush rather than one row per key.
func NewGroupCommit[T any](write func(ctx context.Context, items []T) error, opts ...Option) (*GroupCommit[T], error) {
	if write == nil {
		return nil, ErrNilWriteFunc
	}

	o := newOptions(opts)

	shape, err := resolveShape[T](o)
	if err != nil {
		return nil, err
	}

	g := &GroupCommit[T]{
		write:          write,
		newAccumulator: shape.newAccumulator,
		order:          shape.order,
		clock:          o.clock,
		o11y:           observability.NewObserver(groupCommitName, o.logger, o.tracerProvider),
		flushTimeout:   o.flushTimeout,
		wake:           make(chan struct{}, 1),
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
	}

	if g.flushes, g.sizes, err = buildInstruments(o.metricsProvider, groupCommitName); err != nil {
		return nil, err
	}

	go g.run()

	return g, nil
}

// Submit adds items to the batch currently accepting them and blocks until that
// batch has been written.
//
// Read-your-write holds: when Submit returns nil the items are durably where
// the write function put them, so a read that follows sees them. That is what
// makes this usable from a request handler — the caller gets the guarantee it
// would have had from writing itself, and the process gets one statement
// instead of one per handler.
//
// A caller whose context expires stops waiting but does not cancel the flush:
// the batch is shared, and its other waiters still need it. Those items are
// therefore likely to land anyway, which is the right outcome — the work was
// still worth doing. What the caller loses is the confirmation, not the write,
// so a context error here means "I do not know", not "it did not happen".
//
// Submitting nothing is a no-op rather than an empty flush.
func (g *GroupCommit[T]) Submit(ctx context.Context, items ...T) error {
	if len(items) == 0 {
		return nil
	}

	batch := g.join(items)

	select {
	case <-batch.done:
		return batch.err
	case <-ctx.Done():
		return platformerrors.Wrap(ctx.Err(), "waiting for group commit")
	}
}

// Pending reports how many items the batch now accepting them holds — distinct
// keys, when WithMerge is in play. It is a sampled level and not a
// synchronization point: the flusher may swap the batch out the instant it
// returns.
func (g *GroupCommit[T]) Pending() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.open == nil {
		return 0
	}

	return g.open.items.len()
}

// Close stops the flusher and writes whatever was still accumulating, so a
// caller blocked in Submit during shutdown gets a real answer instead of
// hanging until its own context expires. Safe to call more than once.
//
// The final flush runs on ctx rather than on a timeout of its own: shutdown has
// a deadline the caller owns, and this is the one flush with nobody else's
// waiters to protect.
func (g *GroupCommit[T]) Close(ctx context.Context) error {
	ctx, op := g.o11y.Begin(ctx)
	defer op.End()

	g.stopOnce.Do(func() { close(g.stop) })

	<-g.done // an in-flight flush finishes before run returns

	g.mu.Lock()
	g.closed = true
	batch := g.open
	g.open = nil
	g.mu.Unlock()

	if batch == nil {
		return nil
	}

	batch.err = g.commit(ctx, batch)
	close(batch.done)

	if batch.err != nil {
		return op.Error(batch.err, "flushing the final group commit batch")
	}

	return nil
}

// join merges items into the open batch — creating it if this is the first
// caller — and nudges the flusher. The returned batch is the one those items
// will ride, captured under the same lock that lets the flusher swap it out.
func (g *GroupCommit[T]) join(items []T) *groupBatch[T] {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.closed {
		return closedBatch[T]()
	}

	if g.open == nil {
		g.open = &groupBatch[T]{items: g.newAccumulator(), done: make(chan struct{})}
	}

	g.open.items.add(items)

	batch := g.open

	select {
	case g.wake <- struct{}{}:
	default: // a flush is already pending; this batch is part of it
	}

	return batch
}

// take swaps the open batch out for flushing. Items arriving after this point
// start the next batch.
func (g *GroupCommit[T]) take() *groupBatch[T] {
	g.mu.Lock()
	defer g.mu.Unlock()

	batch := g.open
	g.open = nil

	return batch
}

// run flushes whatever has accumulated, one batch at a time, until close.
func (g *GroupCommit[T]) run() {
	defer close(g.done)

	for {
		select {
		case <-g.stop:
			return
		case <-g.wake:
		}

		g.flush(g.take())
	}
}

// flush writes one batch and releases its waiters. Every exit path closes done
// exactly once — a waiter parked on a batch that never completes would hold a
// request open until its own context expired.
//
// The flush gets its own context rather than any caller's, for the same reason:
// the batch is shared, so the first waiter to give up must not cancel the write
// the rest are still waiting on.
func (g *GroupCommit[T]) flush(batch *groupBatch[T]) {
	if batch == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.flushTimeout)
	defer cancel()

	batch.err = g.commit(ctx, batch)
	close(batch.done)
}

// commit runs one batch through the write function, traced and measured.
//
// It is a span of its own rather than a child of whoever triggered it: the
// write is on behalf of every waiter and belongs to none of them, and parenting
// it under the first caller to arrive would attribute a merged statement's
// latency to whichever request happened to open the batch.
func (g *GroupCommit[T]) commit(ctx context.Context, batch *groupBatch[T]) error {
	items := batch.items.collect()
	if g.order != nil {
		slices.SortStableFunc(items, g.order)
	}

	if len(items) == 0 {
		return nil
	}

	ctx, op := g.o11y.BeginCustom(ctx, groupCommitName+".flush")
	defer op.End()

	op.Set(itemCountKey, len(items))

	g.flushes.Attempt(ctx)
	g.sizes.Record(ctx, float64(len(items)))

	defer op.Time(ctx, g.clock, g.flushes.Latency)()

	if err := g.write(ctx, items); err != nil {
		g.flushes.Failed(ctx)

		return op.Error(err, "writing group commit batch")
	}

	return nil
}

// closedBatch is an already-completed batch carrying the shutdown error.
func closedBatch[T any]() *groupBatch[T] {
	batch := &groupBatch[T]{items: newSliceAccumulator[T](), done: make(chan struct{}), err: ErrClosed}
	close(batch.done)

	return batch
}
