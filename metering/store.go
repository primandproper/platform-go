package metering

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/database"
)

// Entry is one usage record with everything the store needs to file it: which
// window it falls in, and how it folds into that window's total.
//
// The period and aggregation are resolved above the store rather than looked up
// inside it, so the store never consults the Registry. That keeps the persistence
// seam free of the meter catalog, which is the part an application is most likely
// to want to supply differently.
type Entry struct {
	// Bounds is the window OccurredAt resolved to.
	Bounds Bounds

	// Aggregation is the meter's aggregation.
	Aggregation Aggregation

	// Usage is the record itself.
	Usage
}

// RecordResult is what one Record call did.
type RecordResult struct {
	// Accepted is how many records were new and were folded into a total.
	Accepted int

	// Duplicates is how many carried an idempotency key that had already been
	// seen and were therefore ignored.
	//
	// Reported rather than merely tolerated, because the number is diagnostic. A
	// steady trickle is a retrying client working as intended; a step change is a
	// queue redelivering everything, and the difference between the two is
	// visible only in this count.
	Duplicates int
}

// Total is a subject's usage on one meter for one period, as the durable store
// holds it.
type Total struct {
	// PeriodStart and PeriodEnd are the window, half-open.
	PeriodStart time.Time
	PeriodEnd   time.Time

	// LastOccurredAt is the event time of the most recent record folded in. It is
	// what AggregationLast orders by, so an out-of-order record does not displace
	// a newer one.
	LastOccurredAt time.Time

	// NextFlush is when this total may next be posted to the provider.
	NextFlush time.Time

	// Subject and Meter identify whose usage this is.
	Subject string
	Meter   string

	// LastError is why the last flush attempt failed, rendered. Empty otherwise.
	LastError string

	// Aggregation is how the total was folded, stored beside it so a flusher
	// reading the row does not need the registry to interpret it.
	Aggregation Aggregation

	// Quantity is the aggregated total for the period.
	Quantity int64

	// FlushedQuantity is how much of Quantity has already been posted to the
	// provider. The delta between the two is what the next post carries.
	FlushedQuantity int64

	// FlushSequence counts successful posts for this total. It is the varying
	// component of the provider-side idempotency key, which is what makes a
	// retried post a no-op and a genuinely new post distinct.
	FlushSequence int

	// FlushAttempts counts how many times a flusher has claimed this total. It is
	// incremented at claim rather than at failure, so a total that reliably kills
	// its flusher eventually gives up instead of being reclaimed forever.
	FlushAttempts int
}

// Pending reports whether the total has usage the provider has not been told
// about.
func (t *Total) Pending() bool {
	return t != nil && t.Quantity > t.FlushedQuantity
}

// Delta is the quantity the next post carries: everything accumulated since the
// last successful flush.
//
// It is a delta rather than the running total because providers aggregate the
// records within a billing period. Posting a cumulative total on every flush
// would invoice the sum of every partial total ever posted, which for a meter
// flushed every five minutes for a month is roughly nine thousand times the
// right number.
func (t *Total) Delta() int64 {
	if t == nil {
		return 0
	}

	return max(0, t.Quantity-t.FlushedQuantity)
}

// Store is the persistence seam for usage and its totals.
//
// This package ships a SQL implementation (NewSQLStore) together with the DDL it
// needs (metering/migrations), so adopting it does not mean writing this. The
// interface exists because the counting and its storage are genuinely separable,
// and an application with its own schema conventions should not have to fork the
// package to keep them.
//
// Two invariants any implementation must hold, because the rest of the package
// assumes them and neither is checkable from outside:
//
// An idempotency key is recorded at most once, ever, for as long as the event
// ledger retains it. This is the guarantee that keeps a retry from becoming a
// second invoice line, and it must survive process restarts and cache losses —
// which is why it lives here and not in a cache.
//
// Consume is atomic. The read of the total, the decision made against it, and
// the write that follows must be one serialized unit per (subject, meter,
// period), or two concurrent consumers both see room under the limit and both
// take it.
type Store interface {
	// Record durably ingests usage, deduping on idempotency key and folding each
	// new record into its period's total.
	//
	// It is not atomic across entries; see RecordResult.
	Record(ctx context.Context, entries []Entry, at time.Time) (RecordResult, error)

	// RecordTx is Record inside the caller's transaction, so usage commits with
	// whatever produced it.
	//
	// It exists for the call site where the usage and the work are the same fact:
	// a row inserted and the storage it consumes, a message sent and the credit it
	// spends. Recording those separately means a crash between them leaves usage
	// counted for work that rolled back, or work committed that nobody was billed
	// for.
	RecordTx(ctx context.Context, q database.SQLQueryExecutor, entries []Entry, at time.Time) (RecordResult, error)

	// Total reads one subject's total for a meter and period. It returns a zero
	// Total, and no error, for a period nothing has been recorded against — an
	// absent row means no usage, which is a number rather than a missing value.
	Total(ctx context.Context, subject, meter string, bounds Bounds) (*Total, error)

	// Consume atomically decides whether entry may be recorded against a limit,
	// records it if so, and returns the decision.
	//
	// The limit and behavior are passed in rather than looked up, because whose
	// limit applies is a QuotaSource's answer and may differ per subject.
	Consume(ctx context.Context, entry Entry, limit int64, behavior QuotaBehavior, at time.Time) (*Decision, error)

	// ClaimFlushable leases the next batch of totals with usage the provider has
	// not been told about, incrementing their attempt counts.
	ClaimFlushable(ctx context.Context, now time.Time, limit, maxAttempts int, leaseUntil time.Time) ([]*Total, error)

	// MarkFlushed records a successful post: the flushed quantity advances to
	// what was posted and the sequence increments, both in one statement.
	//
	// It is guarded on the sequence the flusher read, so a flusher whose lease
	// lapsed while it was posting cannot advance a sequence a second flusher has
	// already moved. Losing that race is how the same delta gets posted twice
	// under two different keys, which no idempotency key can undo.
	MarkFlushed(ctx context.Context, total *Total, flushed int64, at time.Time) error

	// ReleaseFlush returns a total to the flushable set after a failed post,
	// recording why and when it may be retried.
	ReleaseFlush(ctx context.Context, total *Total, lastErr string, nextFlush time.Time) error

	// ReapEvents deletes usage event rows older than before, up to limit rows,
	// leaving the totals they were folded into untouched.
	//
	// It deletes only events whose period has been fully flushed. An event row
	// removed while its period still owes the provider usage would take the
	// evidence for an invoice line with it, and would let a redelivery of that
	// same event be counted a second time.
	ReapEvents(ctx context.Context, before time.Time, limit int) (int64, error)

	// WithTransaction runs fn against the store's database, for callers using
	// RecordTx.
	WithTransaction(ctx context.Context, fn func(q database.SQLQueryExecutor) error) error
}
