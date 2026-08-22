package workqueue

import (
	"context"
	stderrors "errors"
	"slices"
	"time"

	"github.com/primandproper/platform-go/v13/batching"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
)

// enqueueFlushTimeout bounds one merged upsert. It is generous on purpose: the
// batch's waiters have already given up their own deadlines to it, and an
// enqueue that lands late still schedules the work correctly.
const enqueueFlushTimeout = 10 * time.Second

// Entry is one unit of work being offered to the queue.
type Entry[K comparable] struct {
	// Key names the work. It is the row's identity: enqueueing the same key
	// twice updates one row rather than creating two.
	Key K

	// Priority orders the queue ahead of waiting time. Higher goes first, and
	// re-enqueueing an item can only raise it — an enqueue is a claim on
	// attention, so the loudest caller wins and a later, quieter one cannot
	// demote work somebody else already flagged as urgent.
	//
	// It is the generic form of a demand signal. A read path that discovers it
	// needs a key computed sooner enqueues it with a higher priority; that is
	// the whole mechanism, and it is why Enqueue has to be cheap enough to call
	// from a request handler.
	Priority int

	// Delay holds the item back for this long, measured from the database's
	// now() at the moment the row lands.
	//
	// It is a duration rather than a timestamp for the reason the package
	// documentation opens with: an absolute time would be the caller's clock,
	// and the whole point is that no two processes have to agree on one.
	//
	// Re-enqueueing an outstanding item can only move its availability earlier,
	// mirroring Priority. A completed item is being restarted rather than
	// hurried, so it takes the new delay outright.
	Delay time.Duration
}

// Enqueue offers work to the queue, and returns once those keys are durably in
// it.
//
// Every in-flight Enqueue on this process is merged into a single upsert. The
// caller still blocks until its own keys have landed, so read-your-write holds —
// enqueue, then claim, and the key is there — but however many callers are
// enqueueing at once, exactly one statement is ever in flight. That is what
// makes this safe to call from a request handler: the busier the process gets,
// the larger the batches become and the fewer connections the write path holds,
// which is the opposite of how one-statement-per-caller behaves under the same
// load.
//
// A caller whose context expires stops waiting but does not cancel the flush:
// the batch is shared, and its other waiters still need it. Those keys are
// therefore likely to land anyway, which is the right outcome — the work was
// still worth doing.
//
// Keys are validated before they join a batch, so a malformed key fails its own
// Enqueue rather than poisoning everybody else's.
func (q *Queue[K]) Enqueue(ctx context.Context, entries ...Entry[K]) error {
	ctx, op := q.o11y.Begin(ctx, observability.WithValue(itemCountKey, len(entries)))
	defer op.End()

	if len(entries) == 0 {
		return nil
	}

	rows := make([]encodedEntry, 0, len(entries))

	for i := range entries {
		key, err := encodeKey(q.codec, entries[i].Key)
		if err != nil {
			return op.Error(err, "encoding work queue key")
		}

		delay := max(entries[i].Delay, 0)

		rows = append(rows, encodedEntry{
			key:         key,
			priority:    entries[i].Priority,
			delayMicros: delay.Microseconds(),
		})
	}

	if err := q.batcher.Submit(ctx, rows...); err != nil {
		// Restated in this package's vocabulary. A caller checking for
		// ErrClosed is asking whether this queue is still open, and would not
		// think to reach for the batcher's sentinel to find out; ErrClosed
		// wraps it, so a check against either still holds.
		if stderrors.Is(err, batching.ErrClosed) {
			return op.Error(ErrClosed, "enqueuing work queue items")
		}

		return op.Error(err, "enqueuing work queue items")
	}

	q.enqueuedCounter.Add(ctx, int64(len(entries)), q.attrs)

	return nil
}

// EnqueueKeys is Enqueue for the ordinary case: work with no priority and no
// delay, wanted as soon as a worker is free.
func (q *Queue[K]) EnqueueKeys(ctx context.Context, keys ...K) error {
	entries := make([]Entry[K], 0, len(keys))
	for i := range keys {
		entries = append(entries, Entry[K]{Key: keys[i]})
	}

	return q.Enqueue(ctx, entries...)
}

// upsert writes one merged batch.
func (q *Queue[K]) upsert(ctx context.Context, rows []encodedEntry) error {
	if len(rows) == 0 {
		return nil
	}

	q.enqueueBatchHist.Record(ctx, float64(len(rows)), q.attrs)

	query, args := buildUpsert(q.cfg.resolvedTable(), q.cfg.Name, rows)

	if err := q.retrier.Do(ctx, "enqueue", func() error {
		if _, execErr := q.client.Writer().ExecContext(ctx, query, args...); execErr != nil {
			return platformerrors.Wrap(execErr, "upserting work queue items")
		}

		return nil
	}); err != nil {
		return err
	}

	q.notify(ctx)

	return nil
}

// notify wakes whoever is listening, after the rows are committed and never
// before — a claimer woken early would find nothing and go back to sleep until
// its poll, which is the latency this exists to remove.
//
// A failure here is logged rather than returned. The work is already durably
// enqueued; reporting an error would tell the caller its enqueue failed when it
// did not, and the only consequence of a missing notification is that the item
// waits for a poll — exactly what happens when a listener is reconnecting.
func (q *Queue[K]) notify(ctx context.Context) {
	if q.cfg.NotifyChannel == "" {
		return
	}

	if _, err := q.client.Writer().ExecContext(ctx, dialect.PostgresNotifyStatement, q.cfg.NotifyChannel); err != nil {
		q.o11y.Logger().WithValue(notifyChannelKey, q.cfg.NotifyChannel).Error("notifying work queue channel", err)
	}
}

// newEnqueueBatcher builds the group-commit batcher every Enqueue funnels
// through: one upsert in flight however many callers are enqueueing, one row per
// key however many of them named it, and rows handed to the write in primary-key
// order.
//
// That last part is not a tidiness. The failure this batcher exists to prevent
// is a read path that enqueues on every request issuing one multi-row upsert per
// in-flight request against the same handful of popular rows; those upserts take
// row locks in whatever order each caller happened to build, deadlock against
// each other, and hold a pool connection while they do, until the pool empties
// and endpoints with nothing to do with the queue start failing. Merging under
// one lock order fixes it at the root — see the batching package, which carries
// the long form and the incident it came from.
//
// Options are appended rather than prepended, so a caller — which in practice
// means a test — can override what is set here.
func newEnqueueBatcher(
	write func(ctx context.Context, rows []encodedEntry) error,
	opts ...batching.Option,
) (*batching.GroupCommit[encodedEntry], error) {
	batcher, err := batching.NewGroupCommit(write, append([]batching.Option{
		batching.WithMerge(func(row encodedEntry) string { return row.key }, mergeEntries),
		batching.WithFlushTimeout(enqueueFlushTimeout),
	}, opts...)...)
	if err != nil {
		return nil, platformerrors.Wrap(err, "building work queue enqueue batcher")
	}

	return batcher, nil
}

// mergeEntries folds a new row into whatever the batch already held for that
// key, applying the same rule the ON CONFLICT clause applies in SQL: at least
// this urgent, at least this soon.
//
// It has to agree with that clause exactly. If merging inside the batch were
// more permissive than merging against the table, two callers naming one key
// would get a different result depending on whether they happened to land in the
// same flush — which is the kind of bug that only appears under load.
//
// The batcher only calls it when there is something to fold into, so existing is
// always a row that was really there.
func mergeEntries(existing, incoming encodedEntry) encodedEntry {
	return encodedEntry{
		key:         existing.key,
		priority:    max(existing.priority, incoming.priority),
		delayMicros: min(existing.delayMicros, incoming.delayMicros),
	}
}

// sortAndDedupe puts a batch of encoded keys into primary-key order and removes
// repeats, for the writers that bind keys directly.
func sortAndDedupe(keys []string) []string {
	slices.Sort(keys)

	return slices.Compact(keys)
}
