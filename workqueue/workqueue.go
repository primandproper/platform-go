package workqueue

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/batching"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/pgretry"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Observability keys for this package's spans and log fields. Declared once so
// that a field set on a span and the same field logged alongside it cannot
// drift apart, and so the workqueue. prefix is applied uniformly — an
// un-namespaced attribute name collides with every other component writing to
// the same trace.
const (
	queueNameKey     = "workqueue.queue"
	itemCountKey     = "workqueue.item_count"
	claimedKey       = "workqueue.claimed"
	reclaimedKey     = "workqueue.reclaimed"
	claimLimitKey    = "workqueue.claim_limit"
	leaseKey         = "workqueue.lease"
	attemptKey       = "workqueue.attempt"
	depthKey         = "workqueue.depth"
	readyKey         = "workqueue.ready"
	oldestReadyKey   = "workqueue.oldest_ready_age_seconds"
	reapedKey        = "workqueue.reaped"
	notifyChannelKey = "workqueue.notify_channel"
)

// microsPerMilli converts the microsecond-resolution latency this package
// measures into the milliseconds every other histogram in the module reports.
const microsPerMilli = 1000.0

// Item is one leased unit of work.
//
// It carries no timestamp, deliberately. The lease was just stamped from the
// server's now(), so a deadline expressed against the caller's clock would be
// exactly the process-clock dependency this package exists to remove; if a
// worker needs to know whether it still holds the lease, the answer is to finish
// and let Complete match nothing rather than to compare clocks.
type Item[K comparable] struct {
	// Key names the work. It is the key that was enqueued, decoded back through
	// the queue's codec.
	Key K
	// Priority is the item's current priority, which may be higher than the one
	// it was first enqueued with — re-enqueueing raises it.
	Priority int
	// Attempts counts claims of this item, including this one. It is 1 on a
	// first claim, so a worker can tell a fresh item from a retried one.
	Attempts int
	// Reclaimed reports that this claim took over a lease that lapsed rather
	// than one that was released or completed.
	//
	// It is the only visible trace of the package's failure-recovery mechanism.
	// A steady trickle is healthy — workers do die. A rate that tracks the claim
	// rate means leases are shorter than the work, and every item is being done
	// at least twice.
	Reclaimed bool
}

// Stats is the queue's shape, read in one round trip.
type Stats struct {
	// OldestReadyAge is how long the oldest claimable item has been waiting,
	// measured on the database's clock. Zero when nothing is claimable.
	//
	// This is the number to alert on. Every other field is a level, and no level
	// distinguishes a queue that is deep because it is busy from one that is
	// deep because it has stopped.
	OldestReadyAge time.Duration
	// Pending counts items that have not been completed, whether or not they are
	// currently claimable.
	Pending int64
	// Ready counts items a Claim would hand out right now.
	Ready int64
	// Leased counts items currently held by a worker.
	Leased int64
	// Stalled counts pending items that have exhausted Config.MaxAttempts and
	// will never be claimed again. Always zero when MaxAttempts is unlimited.
	Stalled int64
	// Completed counts finished items still inside the retention window.
	Completed int64
}

// Queue is a leased work queue over one Postgres table.
//
// It is safe for concurrent use, and is meant to be shared: one Queue per
// process per logical queue, handed to every goroutine that enqueues or claims.
// Enqueue's group commit is what makes that sharing pay — a Queue per caller
// would merge nothing.
//
// A Queue owns a goroutine and must be Closed.
type Queue[K comparable] struct {
	// lastWake anchors the wake floor, guarded by wakeMu below because a Queue
	// is shared. It is the one place this package reads a process clock, and it
	// is allowed to: it paces this process's own polling, and no part of the
	// schedule depends on it — which is what the database's now() is for.
	lastWake time.Time
	client   database.Client
	codec    KeyCodec[K]
	o11y     observability.Observer

	enqueuedCounter  metrics.Int64Counter
	claimedCounter   metrics.Int64Counter
	reclaimedCounter metrics.Int64Counter
	completedCounter metrics.Int64Counter
	releasedCounter  metrics.Int64Counter
	removedCounter   metrics.Int64Counter
	reapedCounter    metrics.Int64Counter
	retryCounter     metrics.Int64Counter

	// retrier re-runs a write that Postgres asked to have re-run. See
	// internal/pgretry for why these retries exist in a table this package
	// otherwise locks in a fixed order.
	retrier pgretry.Retrier

	depthGauge      metrics.Int64Gauge
	readyGauge      metrics.Int64Gauge
	stalledGauge    metrics.Int64Gauge
	oldestReadyeAge metrics.Int64Gauge

	claimBatchHist   metrics.Float64Histogram
	enqueueBatchHist metrics.Float64Histogram
	claimLatencyHist metrics.Float64Histogram

	// attrs labels every measurement with the queue name. One process commonly
	// runs several queues against one table, and without this their counters
	// collapse into a single number in which a queue that has stopped draining
	// is invisible beside the ones that are fine.
	attrs metric.MeasurementOption

	batcher *batching.GroupCommit[encodedEntry]

	// wakeup is nil unless WithWakeup supplied one. A nil channel blocks
	// forever in a select, so Wait needs no branch for its absence.
	wakeup <-chan struct{}

	cfg Config

	wakeMu sync.Mutex
}

// New builds a Queue over client, which must speak Postgres and must be the
// database holding the queue table.
//
// ctx is used to validate the config and is not retained; every method takes its
// own.
func New[K comparable](
	ctx context.Context,
	cfg *Config,
	client database.Client,
	opts ...Option,
) (*Queue[K], error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if err := dialect.RequirePostgres("work queue", client.Dialect()); err != nil {
		return nil, err
	}

	cfg.EnsureDefaults()

	if cfg.Name == "" {
		return nil, ErrEmptyQueueName
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating work queue config")
	}

	if !dialect.ValidIdentifier(cfg.resolvedTable()) {
		return nil, platformerrors.Wrapf(dialect.ErrInvalidIdentifier, "work queue table %q", cfg.resolvedTable())
	}

	// The channel is bound as text by the statement this package emits, but the
	// listener on the other end has to render it into a LISTEN, which takes no
	// parameters. Vetting it here is what keeps that end from having to.
	if cfg.NotifyChannel != "" && !dialect.ValidIdentifier(cfg.NotifyChannel) {
		return nil, platformerrors.Wrapf(dialect.ErrInvalidIdentifier, "work queue notify channel %q", cfg.NotifyChannel)
	}

	var (
		o   = newQueueOptions(opts)
		err error
	)

	q := &Queue[K]{
		cfg:    *cfg,
		client: client,
		codec:  DefaultKeyCodec[K](),
		wakeup: o.wakeup,
		attrs:  metric.WithAttributes(attribute.String(queueNameKey, cfg.Name)),
	}

	// Asserted rather than assumed: Option cannot name K, so this is where a
	// codec built for another key type is caught. Failing here means it is
	// caught at construction, before a single key has been written to the table
	// under a rendering nothing will ever decode.
	if o.keyCodec != nil {
		codec, ok := o.keyCodec.(KeyCodec[K])
		if !ok {
			return nil, platformerrors.Wrapf(ErrKeyCodecTypeMismatch,
				"codec is %T, want KeyCodec[%T]", o.keyCodec, *new(K))
		}

		q.codec = codec
	}

	// Every operation this queue performs is about this one queue, so the name
	// is stated once here instead of at each Begin below.
	q.o11y = observability.NewObserverWithValues(serviceName, o.logger, o.tracerProvider,
		map[string]any{queueNameKey: cfg.Name})

	if err = q.buildInstruments(o.metricsProvider); err != nil {
		return nil, err
	}

	q.retrier = pgretry.Retrier{
		Logger:     q.o11y.Logger(),
		Counter:    q.retryCounter,
		AddOptions: []metric.AddOption{q.attrs},
		AttemptKey: attemptKey,
		Subject:    "work queue",
		Attempts:   q.cfg.WriteAttempts,
	}

	// The flush deliberately runs on a context of its own rather than any
	// caller's; see batching.GroupCommit for why a shared batch cannot inherit
	// one waiter's cancellation.
	if q.batcher, err = newEnqueueBatcher(q.upsert, //nolint:contextcheck // see batching.GroupCommit
		batching.WithLogger(o.logger),
		batching.WithTracerProvider(o.tracerProvider),
		batching.WithMetricsProvider(o.metricsProvider),
	); err != nil {
		return nil, err
	}

	return q, nil
}

// buildInstruments creates every metric the queue records. Split out of New
// because it is a wall of near-identical error handling that says nothing about
// how a Queue is assembled.
func (q *Queue[K]) buildInstruments(metricsProvider metrics.Provider) error {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	counters := []struct {
		into *metrics.Int64Counter
		name string
	}{
		{&q.enqueuedCounter, "items_enqueued"},
		{&q.claimedCounter, "items_claimed"},
		{&q.reclaimedCounter, "leases_expired"},
		{&q.completedCounter, "items_completed"},
		{&q.releasedCounter, "items_released"},
		{&q.removedCounter, "items_removed"},
		{&q.reapedCounter, "items_reaped"},
		{&q.retryCounter, "write_retries"},
	}
	for _, c := range counters {
		instrument, err := mp.NewInt64Counter(fmt.Sprintf("%s_%s", serviceName, c.name))
		if err != nil {
			return platformerrors.Wrapf(err, "creating %s counter", c.name)
		}

		*c.into = instrument
	}

	gauges := []struct {
		into *metrics.Int64Gauge
		name string
	}{
		{&q.depthGauge, "depth"},
		{&q.readyGauge, "ready_depth"},
		{&q.stalledGauge, "stalled_depth"},
		{&q.oldestReadyeAge, "oldest_ready_age_seconds"},
	}
	for _, g := range gauges {
		instrument, err := mp.NewInt64Gauge(fmt.Sprintf("%s_%s", serviceName, g.name))
		if err != nil {
			return platformerrors.Wrapf(err, "creating %s gauge", g.name)
		}

		*g.into = instrument
	}

	histograms := []struct {
		into *metrics.Float64Histogram
		name string
	}{
		{&q.claimBatchHist, "claimed_batch_size"},
		{&q.enqueueBatchHist, "enqueue_batch_size"},
		{&q.claimLatencyHist, "claim_latency_ms"},
	}
	for _, h := range histograms {
		instrument, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_%s", serviceName, h.name))
		if err != nil {
			return platformerrors.Wrapf(err, "creating %s histogram", h.name)
		}

		*h.into = instrument
	}

	return nil
}

// Name reports the logical queue this Queue reads and writes.
func (q *Queue[K]) Name() string {
	return q.cfg.Name
}

// Close stops the enqueue batcher, writing whatever it still holds so that a
// caller blocked in Enqueue during shutdown gets a real answer instead of
// hanging until its own context expires.
//
// Safe to call more than once. Enqueue after Close returns ErrClosed; every
// other method keeps working, because they hold no state of their own — a
// worker draining its last claimed batch does not need the batcher.
func (q *Queue[K]) Close(ctx context.Context) error {
	ctx, op := q.o11y.Begin(ctx)
	defer op.End()

	if err := q.batcher.Close(ctx); err != nil {
		return op.Error(err, "closing work queue")
	}

	return nil
}

// Wait paces a claim loop: it blocks until a wakeup arrives, until poll
// elapses, or until ctx is done, whichever comes first.
//
// It is the one piece of the loop this package supplies, and it exists because
// the loop is otherwise the caller's:
//
//	for {
//		items, err := queue.Claim(ctx, 10, time.Minute)
//		// ...
//		if len(items) == 0 {
//			if err = queue.Wait(ctx, time.Second); err != nil {
//				return err
//			}
//		}
//	}
//
// Without WithWakeup it is a sleep, and a loop written around it behaves
// exactly as one written around time.Sleep. With one, an enqueue that lands a
// millisecond after a poll is claimed a millisecond later instead of a poll
// interval later, and an idle worker stops issuing a claim per tick — which is
// the larger win, because idle is what a work queue mostly is.
//
// poll must be positive. It is the backstop that makes the wakeup safe to lose,
// and losing wakes is normal: the signal is at-most-once, and a listener that
// reconnects misses whatever arrived while it was away. A loop with no backstop
// would stop forever the first time that happened.
//
// Config.MinWakeInterval floors how often a wake can return, so a burst of
// enqueues costs one extra claim rather than one per enqueue. Wait holds the
// wake for the remainder of the interval rather than discarding it, so the last
// enqueue of a burst is still claimed promptly.
//
// Call it from one loop per Queue. A wake goes to a single receiver, so several
// loops sharing a Queue would divide the wakes between them arbitrarily and run
// on their poll intervals the rest of the time — correct, but pointless. Give
// each loop its own Queue and its own Listener.
//
// Wait is not traced. A span per poll would be a root span per idle tick, which
// is the same noise Claim declines to emit when it leases nothing.
func (q *Queue[K]) Wait(ctx context.Context, poll time.Duration) error {
	if poll <= 0 {
		return ErrInvalidPollInterval
	}

	timer := time.NewTimer(poll)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return platformerrors.Wrap(ctx.Err(), "waiting for work queue")
	case <-timer.C:
		return nil
	case <-q.wakeup:
		hold := q.reserveWake()
		if hold <= 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return platformerrors.Wrap(ctx.Err(), "waiting out the work queue wake floor")
		case <-time.After(hold):
			return nil
		}
	}
}

// reserveWake claims the next wake slot and reports how long the caller must
// hold before taking it.
//
// The reservation is what bounds a burst: the slot moves forward whether or not
// the caller had to wait, so a second waker arriving during the hold is pushed
// behind the first rather than joining it.
func (q *Queue[K]) reserveWake() time.Duration {
	q.wakeMu.Lock()
	defer q.wakeMu.Unlock()

	now := time.Now()

	if earliest := q.lastWake.Add(q.cfg.MinWakeInterval); now.Before(earliest) {
		q.lastWake = earliest

		return earliest.Sub(now)
	}

	q.lastWake = now

	return 0
}

// Claim leases up to limit of the queue's due items for the given lease
// duration, in one statement: nothing is selected without also being leased, so
// two claimers can never see the same item.
//
// Due means unfinished, unleased, past its delay, and — when Config.MaxAttempts
// is set — not yet out of attempts. Ties are broken by priority first and by
// waiting time second, so the loudest and then the oldest work goes first.
//
// The limit counts items actually leased, so a full batch comes back whenever
// that many items are due — a row another claimer holds is skipped and replaced,
// not subtracted. A short batch therefore means the queue is nearly drained, and
// an empty one means it is.
//
// The lease is what the caller promises to finish inside. There is no
// heartbeat and no way to extend one — a lease that lapses mid-work hands the
// item to somebody else, and the original worker's eventual Complete lands on an
// item that is already done. That is waste, not corruption, provided the work is
// idempotent; if it is not, this package is the wrong tool.
func (q *Queue[K]) Claim(ctx context.Context, limit int, lease time.Duration) ([]Item[K], error) {
	ctx, op := q.o11y.Begin(ctx, observability.WithValues(map[string]any{
		claimLimitKey: limit,
		leaseKey:      lease.String(),
	}))
	defer op.End()

	if lease <= 0 {
		return nil, op.Error(ErrInvalidLease, "claiming work queue items")
	}

	if limit <= 0 || limit > q.cfg.MaxClaimBatch {
		limit = q.cfg.MaxClaimBatch
	}

	startTime := time.Now()

	items, err := q.claim(ctx, limit, lease)
	if err != nil {
		return nil, op.Error(err, "claiming work queue items")
	}

	q.claimLatencyHist.Record(ctx, float64(time.Since(startTime).Microseconds())/microsPerMilli, q.attrs)

	if len(items) == 0 {
		return nil, nil
	}

	var reclaimed int64
	for i := range items {
		if items[i].Reclaimed {
			reclaimed++
		}
	}

	q.claimedCounter.Add(ctx, int64(len(items)), q.attrs)
	q.claimBatchHist.Record(ctx, float64(len(items)), q.attrs)

	if reclaimed > 0 {
		q.reclaimedCounter.Add(ctx, reclaimed, q.attrs)
	}

	op.SetValues(map[string]any{claimedKey: len(items), reclaimedKey: reclaimed})

	return items, nil
}

// claim runs the statement and decodes the result. Separated from Claim so that
// the retry wrapper has a single unit to re-run: a partially scanned result set
// from a deadlocked statement must be discarded, not merged with the retry's.
func (q *Queue[K]) claim(ctx context.Context, limit int, lease time.Duration) ([]Item[K], error) {
	var items []Item[K]

	err := q.retrier.Do(ctx, "claim", func() error {
		var claimErr error

		items, claimErr = q.claimOnce(ctx, limit, lease)

		return claimErr
	})

	return items, err
}

// claimOnce is one attempt of claim.
func (q *Queue[K]) claimOnce(ctx context.Context, limit int, lease time.Duration) ([]Item[K], error) {
	// The writer, not the reader: this is an UPDATE that happens to return rows,
	// and a read replica would both fail it and lose every lease it handed out.
	items, err := database.ScanAll(ctx, q.client.Writer(), "claimed work queue",
		buildClaim(q.cfg.resolvedTable()),
		[]any{q.cfg.Name, q.cfg.attemptCeiling(), limit, lease.Microseconds()},
		func(scanner database.Scanner) (Item[K], error) {
			var (
				encoded string
				item    Item[K]
			)

			if scanErr := scanner.Scan(&encoded, &item.Priority, &item.Attempts, &item.Reclaimed); scanErr != nil {
				return item, platformerrors.Wrap(scanErr, "scanning claimed work queue item")
			}

			// A key that will not decode is the one failure here a caller cannot
			// act on and must not be hidden: it means the table holds rows written
			// under a different key type or codec, and every claim will keep
			// leasing them. Failing the whole batch is the loud version of that,
			// and the lease lapses on its own.
			var decodeErr error
			if item.Key, decodeErr = q.codec.DecodeKey(encoded); decodeErr != nil {
				return item, platformerrors.Wrapf(decodeErr, "decoding claimed work queue key %q", encoded)
			}

			return item, nil
		})
	if err != nil {
		return nil, platformerrors.Wrap(err, "leasing work queue items")
	}

	return items, nil
}

// Complete retires finished items: the lease is dropped and the item stops being
// claimable. Rows are marked rather than deleted so a duplicate or a gap can be
// investigated afterwards; Reap removes them once they age past
// Config.Retention.
//
// Keys the queue does not hold are ignored rather than reported. A straggler
// whose lease lapsed, and whose item was completed by somebody else or removed
// outright, has nothing useful to do with an error.
//
// Completing is idempotent, and re-enqueueing a completed key restarts it with a
// fresh attempt count.
func (q *Queue[K]) Complete(ctx context.Context, keys ...K) error {
	ctx, op := q.o11y.Begin(ctx, observability.WithValue(itemCountKey, len(keys)))
	defer op.End()

	affected, err := q.writeKeys(ctx, "complete", keys, func(encoded []string) (string, []any) {
		args := make([]any, 0, len(encoded)+1)
		args = append(args, q.cfg.Name)

		for _, key := range encoded {
			args = append(args, key)
		}

		return buildComplete(q.cfg.resolvedTable(), len(encoded)), args
	})
	if err != nil {
		return op.Error(err, "completing work queue items")
	}

	q.completedCounter.Add(ctx, affected, q.attrs)

	return nil
}

// Release hands claimed items back to the queue before their leases lapse,
// holding each for delay before it becomes claimable again and recording cause
// as the item's last error.
//
// A zero delay and a nil cause is the plain hand-back — "I am not going to get
// to this" — and needs no ceremony. A non-zero delay is how a caller backs off a
// failing item without a scheduler of its own; retry/config's DelayFor computes
// the same schedule the rest of this module retries on.
//
// Releasing is optional. An unreleased lease lapses and the item returns
// anyway, just later, which is why nothing here treats a failed Release as
// fatal. What it buys is the delay and the recorded reason: without it, a
// failing item comes straight back and spins against whatever it failed on.
//
// Items that have already been completed are skipped, so a late Release arriving
// after somebody else finished the work cannot resurrect it.
func (q *Queue[K]) Release(ctx context.Context, delay time.Duration, cause error, keys ...K) error {
	ctx, op := q.o11y.Begin(ctx, observability.WithValue(itemCountKey, len(keys)))
	defer op.End()

	delay = max(delay, 0)

	affected, err := q.writeKeys(ctx, "release", keys, func(encoded []string) (string, []any) {
		args := make([]any, 0, len(encoded)+3)
		args = append(args, q.cfg.Name, delay.Microseconds(), pgretry.TruncateError(cause))

		for _, key := range encoded {
			args = append(args, key)
		}

		return buildRelease(q.cfg.resolvedTable(), len(encoded)), args
	})
	if err != nil {
		return op.Error(err, "releasing work queue items")
	}

	q.releasedCounter.Add(ctx, affected, q.attrs)

	return nil
}

// Remove drops items from the working set entirely, completed or not.
//
// It is how a queue shrinks: a key whose subject no longer exists should stop
// being scheduled, not be completed as though the work had been done. Removing
// an item somebody currently holds a lease on is allowed — their Complete simply
// matches nothing, exactly as it would after a lapsed lease.
func (q *Queue[K]) Remove(ctx context.Context, keys ...K) error {
	ctx, op := q.o11y.Begin(ctx, observability.WithValue(itemCountKey, len(keys)))
	defer op.End()

	affected, err := q.writeKeys(ctx, "remove", keys, func(encoded []string) (string, []any) {
		args := make([]any, 0, len(encoded)+1)
		args = append(args, q.cfg.Name)

		for _, key := range encoded {
			args = append(args, key)
		}

		return buildRemove(q.cfg.resolvedTable(), len(encoded)), args
	})
	if err != nil {
		return op.Error(err, "removing work queue items")
	}

	q.removedCounter.Add(ctx, affected, q.attrs)

	return nil
}

// Reap deletes completed items that have aged past Config.Retention, up to
// Config.ReapBatchSize of them, and reports how many it removed.
//
// It is a method rather than a loop this package runs, because a consumer
// already has a scheduler — see the jobs package — and a component that starts
// its own timers is a component that has to be told when to stop. Call it on a
// period comfortably shorter than the time it takes ReapBatchSize completions to
// accumulate; a return value equal to the batch size means the queue is falling
// behind on retention and the period is too long.
func (q *Queue[K]) Reap(ctx context.Context) (int64, error) {
	ctx, op := q.o11y.Begin(ctx)
	defer op.End()

	var affected int64

	err := q.retrier.Do(ctx, "reap", func() error {
		res, execErr := q.client.Writer().ExecContext(ctx, buildReap(q.cfg.resolvedTable()),
			q.cfg.Name, q.cfg.Retention.Microseconds(), q.cfg.ReapBatchSize)
		if execErr != nil {
			return platformerrors.Wrap(execErr, "reaping completed work queue items")
		}

		affected, execErr = res.RowsAffected()

		return execErr
	})
	if err != nil {
		return 0, op.Error(err, "reaping work queue items")
	}

	op.Set(reapedKey, affected)

	if affected > 0 {
		q.reapedCounter.Add(ctx, affected, q.attrs)
	}

	return affected, nil
}

// Stats reads the queue's shape and records it to the gauges.
//
// It is the health signal: nothing in this package fails loudly, so a queue that
// has stopped draining looks exactly like an idle one until somebody counts what
// is waiting. Sample it on a timer rather than per claim — every field is an
// aggregate over the queue, and at claim cadence the read costs more than the
// work it reports on.
func (q *Queue[K]) Stats(ctx context.Context) (Stats, error) {
	ctx, op := q.o11y.Begin(ctx)
	defer op.End()

	var (
		stats     Stats
		oldestMic int64
	)

	if err := q.client.Reader().
		QueryRowContext(ctx, buildStats(q.cfg.resolvedTable()), q.cfg.Name, q.cfg.attemptCeiling()).
		Scan(&stats.Pending, &stats.Ready, &stats.Leased, &stats.Stalled, &stats.Completed, &oldestMic); err != nil {
		return Stats{}, op.Error(err, "reading work queue stats")
	}

	if oldestMic > 0 {
		stats.OldestReadyAge = time.Duration(oldestMic) * time.Microsecond
	}

	oldestSeconds := int64(stats.OldestReadyAge.Seconds())

	q.depthGauge.Record(ctx, stats.Pending, q.attrs)
	q.readyGauge.Record(ctx, stats.Ready, q.attrs)
	q.stalledGauge.Record(ctx, stats.Stalled, q.attrs)
	q.oldestReadyeAge.Record(ctx, oldestSeconds, q.attrs)

	op.SetValues(map[string]any{
		depthKey:       stats.Pending,
		readyKey:       stats.Ready,
		oldestReadyKey: oldestSeconds,
	})

	return stats, nil
}

// writeKeys is the shape every keyed writer shares: encode the keys, hand the
// encoded batch to build, run it with the retry wrapper, and report how many
// rows it touched.
//
// The keys are sorted before the statement is built. That is the lock-ordering
// discipline, applied at the one place all three writers pass through, so a
// fourth added later inherits it rather than having to remember it.
func (q *Queue[K]) writeKeys(
	ctx context.Context,
	label string,
	keys []K,
	build func(encoded []string) (query string, args []any),
) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}

	encoded, err := encodeKeys(q.codec, keys)
	if err != nil {
		return 0, err
	}

	encoded = sortAndDedupe(encoded)

	query, args := build(encoded)

	var affected int64

	err = q.retrier.Do(ctx, label, func() error {
		res, execErr := q.client.Writer().ExecContext(ctx, query, args...)
		if execErr != nil {
			return execErr
		}

		affected, execErr = res.RowsAffected()

		return execErr
	})

	return affected, err
}
