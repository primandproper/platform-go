package searchsync

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v13/batching"
)

// Stamper records that the index accepted documents, so the rows behind them
// can be marked as indexed.
//
// It is one method, and it is deliberately the non-blocking, error-free one.
// Stamping is a side effect of applying an event: the event has already been
// applied by the time it happens, nothing reads the column back in the same
// breath, and a Syncer that failed an event because it could not update a
// bookkeeping timestamp would dead-letter a document the index is holding
// correctly.
//
// *batching.Buffer[string] satisfies it as it stands, which is what it is
// shaped for — see NewStampBuffer. An application stamping through something
// else implements one method.
type Stamper interface {
	// Add records that the index now holds the documents with these ids.
	Add(ids ...string)
}

// NewStampBuffer builds the buffered writer a Syncer stamps through: keys
// coalesced in memory and flushed through write on an interval or when the
// buffer fills.
//
// write is handed the whole flushed set rather than one id at a time, because
// one statement per flush is the entire reason the write is buffered. Its
// natural implementation is the bulk stamp querygen emits for any table
// carrying last_indexed_at — MarkXAsIndexed, an UPDATE over WHERE id = ANY —
// so the two halves fit together without the application writing either:
//
//	stamps, err := searchsync.NewStampBuffer(func(ctx context.Context, ids []string) error {
//	    _, err := queries.MarkOrdersAsIndexed(ctx, db, ids)
//
//	    return err
//	}, batching.WithLogger(logger), batching.WithMetricsProvider(metricsProvider))
//	if err != nil {
//	    return err
//	}
//
//	defer func() { _ = stamps.Close(shutdownCtx) }()
//
//	syncer, err := searchsync.NewSyncer("orders", orderSource, target,
//	    searchsync.WithSyncerStamper(stamps))
//
// Buffered rather than direct, and that is not a throughput optimization. One
// UPDATE per applied document, issued from every worker of a jobs.Pool at once,
// is concurrent statements taking row locks on the same popular rows in
// whatever order each caller built them in — Postgres 40P01, holding a pool
// connection while it deadlocks, until endpoints with nothing to do with that
// table start failing. A Buffer collapses the repeats, flushes from one
// goroutine, and emits in id order: one stamping write in flight, one lock
// order.
//
// That ordering is pinned here rather than left to opts, because it is the half
// that is load-bearing and the half that looks optional. Everything else about
// the buffer — its interval, its flush timeout, how many ids it holds before
// flushing early, and the three observability pillars — is the caller's, and a
// batching.Option this constructor has no use for is accepted and ignored just
// as batching's own constructors accept it.
//
// A Buffer owns a goroutine and must be Closed, which is why it is returned to
// the caller rather than built inside NewSyncer. A Syncer owns no goroutine and
// has no lifecycle; acquiring one through an option would be a shutdown
// obligation that nothing in its signature mentions.
func NewStampBuffer(write func(ctx context.Context, ids []string) error, opts ...batching.Option) (*batching.Buffer[string], error) {
	// Appended last so it wins: the ordering is what makes the buffered write
	// safe to run beside every other writer of those rows, and an opts slice
	// assembled elsewhere must not be able to unset it.
	return batching.NewBuffer(write, append(opts, batching.WithOrder(strings.Compare))...)
}
