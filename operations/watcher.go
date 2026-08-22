package operations

import (
	"context"
	"sync"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
)

// watcherName scopes the watcher's spans and logger.
const watcherName = serviceName + "_watcher"

// subscription is one caller's view of one operation.
//
// Everything mutable on it is guarded by its own mutex rather than by the
// Watcher's, because the two writers are genuinely unrelated: Watch delivers the
// first snapshot from the caller's goroutine while the watch loop may already be
// delivering the next from its own, and the loop must not be serialized behind
// every subscriber's first read.
type subscription struct {
	// out is the caller's channel, capacity one. See Watcher.deliver on why one
	// is enough.
	out chan *Operation

	// retired closes when the subscription is finished with, so the goroutine
	// waiting on the caller's context exits with it rather than outliving it.
	retired chan struct{}

	id string

	// sent is the revision most recently delivered, so an unchanged row costs
	// nothing after the re-read.
	sent int64

	// done records that out has been closed, which is what makes closing it
	// exactly once a property of this type rather than of every call site.
	done bool

	mu sync.Mutex
}

func newSubscription(id string) *subscription {
	return &subscription{
		id:      id,
		out:     make(chan *Operation, 1),
		retired: make(chan struct{}),
	}
}

// push delivers a snapshot if it is newer than the last one, reporting whether
// it did.
//
// The send is non-blocking and, when the buffer is full, the stale value is
// drained and replaced. That is safe precisely because these are snapshots: the
// value being dropped says nothing the one replacing it does not. A subscriber
// that has stopped reading therefore costs one operation's worth of memory
// rather than blocking the loop that serves everybody else.
func (s *subscription) push(op *Operation) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.done || op.Revision <= s.sent {
		return false
	}

	s.sent = op.Revision

	select {
	case s.out <- op:
	default:
		select {
		case <-s.out:
		default:
		}

		select {
		case s.out <- op:
		default:
		}
	}

	return true
}

// finish closes the caller's channel, at most once, and releases whatever is
// waiting on this subscription.
//
// It is safe to call from either the watch loop or the context goroutine, which
// is what lets both of them race to end a subscription without either having to
// know whether it won.
func (s *subscription) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.done {
		return
	}

	s.done = true

	close(s.out)
	close(s.retired)
}

// Watcher turns the operations table into a push.
//
// A caller subscribes to an operation and receives a snapshot of it whenever it
// changes, ending with the terminal one. It is what an SSE endpoint is built
// from, and what a test of a long-running flow waits on.
//
// # Snapshots, not deltas
//
// Every value delivered is the whole operation as the row stood when it was
// read, and that single decision is what makes the rest of this cheap. A slow
// subscriber does not need every intermediate state, because the newest snapshot
// contains everything the ones it missed would have said — so the channel holds
// one value, latest wins, and nothing has to be buffered or replayed. A delta
// stream would have had to guarantee delivery of every step, which over a
// connection that can drop means sequence numbers, replay buffers, and a
// retention policy for them.
//
// The terminal snapshot is the exception that costs nothing: it is the last one
// written, so it is either delivered or sitting in the buffer when the channel
// closes, and a receiver draining a closed channel gets it either way.
//
// # One query per wake
//
// A notification carries no payload — see database/postgres/pgnotify on why
// nothing may depend on one that is allowed to be lost — so a wake says only
// "something changed". The loop re-reads every operation it is following in a
// single statement and compares revisions. That is one query per wake regardless
// of how many subscribers there are, which is the property that lets a watcher
// be shared by a whole process.
//
// A Watcher owns a goroutine and must be Closed.
type Watcher struct {
	store Store
	o11y  observability.Observer

	subscriptions map[string][]*subscription

	wakeup <-chan struct{}

	done chan struct{}

	subscriptionsGauge metrics.Int64Gauge
	readCounter        metrics.Int64Counter
	deliveredCounter   metrics.Int64Counter

	cfg WatcherConfig

	closeOnce sync.Once
	closed    bool

	mu sync.Mutex
}

// NewWatcher builds a Watcher over a store.
//
// Without WithWatcherWakeup it polls at WatcherConfig.Poll, which is a complete
// implementation rather than a degraded one — see the Watcher documentation on
// snapshots. With one, a change is delivered in about as long as it takes
// Postgres to deliver a notification.
func NewWatcher(ctx context.Context, cfg *WatcherConfig, store Store, opts ...WatcherOption) (*Watcher, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}
	if store == nil {
		return nil, ErrNilStore
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating operations watcher config")
	}

	o := newWatcherOptions(opts)

	w := &Watcher{
		cfg:           *cfg,
		store:         store,
		wakeup:        o.wakeup,
		subscriptions: map[string][]*subscription{},
		done:          make(chan struct{}),
		o11y:          observability.NewObserver(watcherName, o.logger, o.tracerProvider),
	}
	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	var err error

	if w.subscriptionsGauge, err = mp.NewInt64Gauge(watcherName + "_subscriptions"); err != nil {
		return nil, platformerrors.Wrap(err, "creating operations watcher subscription gauge")
	}

	// Reads against deliveries is the reading that says whether the wake channel
	// is earning its keep: a watcher waking constantly and delivering nothing is
	// one whose notify channel is shared with something noisier than it needs.
	if w.readCounter, err = mp.NewInt64Counter(watcherName + "_reads"); err != nil {
		return nil, platformerrors.Wrap(err, "creating operations watcher read counter")
	}

	if w.deliveredCounter, err = mp.NewInt64Counter(watcherName + "_snapshots_delivered"); err != nil {
		return nil, platformerrors.Wrap(err, "creating operations watcher delivery counter")
	}

	return w, nil
}

// Watch subscribes to one operation and returns the channel its snapshots
// arrive on.
//
// The current state is delivered immediately, before Watch returns, so a caller
// that subscribes to an operation which has already finished receives its
// terminal snapshot and a closed channel rather than waiting for a change that
// will never come. That ordering is the difference between a working status
// endpoint and one that hangs on exactly the requests that are easiest to serve.
//
// The channel is closed when the operation reaches a terminal state, when ctx is
// done, or when the Watcher is closed. Draining it to completion is how a caller
// unsubscribes; abandoning it leaks a subscription until ctx is done, which is
// why ctx should be the request's.
//
// It returns an error wrapping ErrOperationNotFound for an operation that is not
// in the table, and ErrTooManyWatchers past WatcherConfig.MaxSubscriptions.
func (w *Watcher) Watch(ctx context.Context, id string) (<-chan *Operation, error) {
	ctx, span := w.o11y.Begin(ctx, observability.WithValue(operationIDKey, id))
	defer span.End()

	// Read before subscribing, so an unknown ID is an error the caller can
	// render rather than a channel that closes for no stated reason.
	op, err := w.store.Get(ctx, id)
	if err != nil {
		return nil, span.Error(err, "watching operation")
	}

	sub := newSubscription(id)

	if err = w.subscribe(sub); err != nil {
		return nil, span.Error(err, "watching operation")
	}

	// Delivered before returning, so a caller that subscribes to a finished
	// operation is handed its outcome rather than waiting for a change that will
	// never come. The watch loop may already be delivering the next snapshot
	// concurrently; push is what makes the two orderings the same.
	w.push(ctx, sub, op)

	if op.Terminal() {
		w.retire(sub)

		return sub.out, nil
	}

	// One goroutine per subscription, and it does nothing but wait for the
	// caller to go away. The alternative — having the watch loop check every
	// subscription's context on every pass — turns a loop that costs one query
	// per change into one that costs work per subscriber on a timer that fires
	// whether or not anything happened.
	go func() {
		select {
		case <-ctx.Done():
		case <-w.done:
		case <-sub.retired:
		}

		w.retire(sub)
	}()

	return sub.out, nil
}

// retire removes a subscription and closes its channel. Both halves are
// idempotent, so the watch loop and the context goroutine may both call it.
func (w *Watcher) retire(sub *subscription) {
	w.unsubscribe(sub)
	sub.finish()
}

// subscribe registers a subscription, bounded by MaxSubscriptions.
func (w *Watcher) subscribe(sub *subscription) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return ErrWatcherClosed
	}

	if w.count() >= w.cfg.MaxSubscriptions {
		return platformerrors.Wrapf(ErrTooManyWatchers,
			"%d subscriptions, limit %d", w.count(), w.cfg.MaxSubscriptions)
	}

	w.subscriptions[sub.id] = append(w.subscriptions[sub.id], sub)

	return nil
}

// unsubscribe removes a subscription from the registry, if it is still there.
func (w *Watcher) unsubscribe(sub *subscription) {
	w.mu.Lock()
	defer w.mu.Unlock()

	subs := w.subscriptions[sub.id]

	for i := range subs {
		if subs[i] != sub {
			continue
		}

		w.subscriptions[sub.id] = append(subs[:i], subs[i+1:]...)
		if len(w.subscriptions[sub.id]) == 0 {
			delete(w.subscriptions, sub.id)
		}

		return
	}
}

// count reports the total subscription count. Callers hold mu.
func (w *Watcher) count() int {
	total := 0
	for _, subs := range w.subscriptions {
		total += len(subs)
	}

	return total
}

// Run drives the watch loop until ctx is done or the Watcher is closed.
//
// It must be running for any subscription to receive anything after its first
// snapshot. Start it once, from wherever the rest of the process's background
// work starts.
func (w *Watcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.Poll)
	defer ticker.Stop()

	// The floor between reads, held here rather than as a timestamp compared
	// against a clock, because this is the one place in the package that paces
	// this process's own polling and nothing in the schedule depends on it.
	var lastRead time.Time

	for {
		select {
		case <-ctx.Done():
			return platformerrors.Wrap(ctx.Err(), "running the operations watcher")
		case <-w.done:
			return nil
		case <-ticker.C:
		case <-w.wakeup:
			// A busy fleet emits a notification per progress flush per
			// operation. Without this floor a watcher would issue one read for
			// every one of them, which is the spin the wake was meant to save.
			if since := time.Since(lastRead); since < w.cfg.MinReadInterval {
				select {
				case <-ctx.Done():
					return platformerrors.Wrap(ctx.Err(), "running the operations watcher")
				case <-w.done:
					return nil
				case <-time.After(w.cfg.MinReadInterval - since):
				}
			}
		}

		lastRead = time.Now()

		w.sweep(ctx)
	}
}

// sweep re-reads every subscribed operation and delivers what changed.
func (w *Watcher) sweep(ctx context.Context) {
	ids := w.watchedIDs()
	if len(ids) == 0 {
		return
	}

	w.readCounter.Add(ctx, 1)
	w.subscriptionsGauge.Record(ctx, int64(w.total()))

	ops, err := w.store.GetMany(ctx, ids)
	if err != nil {
		// Logged and slept off. A watcher that returned here would stop
		// delivering to every subscriber because one read failed, and the next
		// tick is a couple of seconds away.
		w.o11y.Logger().Error("re-reading watched operations", err)

		return
	}

	for _, op := range ops {
		for _, sub := range w.subscribersOf(op.ID) {
			w.push(ctx, sub, op)

			if op.Terminal() {
				w.retire(sub)
			}
		}
	}
}

// push delivers a snapshot and counts it, if it was newer than the last.
func (w *Watcher) push(ctx context.Context, sub *subscription, op *Operation) {
	if sub.push(op) {
		w.deliveredCounter.Add(ctx, 1)
	}
}

// watchedIDs snapshots the subscribed IDs.
func (w *Watcher) watchedIDs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	ids := make([]string, 0, len(w.subscriptions))
	for id := range w.subscriptions {
		ids = append(ids, id)
	}

	return ids
}

// total reports the subscription count for the gauge.
func (w *Watcher) total() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.count()
}

// subscribersOf snapshots the subscriptions for one operation.
func (w *Watcher) subscribersOf(id string) []*subscription {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]*subscription(nil), w.subscriptions[id]...)
}

// Close stops the watch loop and closes every subscription.
//
// It is idempotent, and Watch returns ErrWatcherClosed afterwards.
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()

		close(w.done)
	})

	return nil
}
