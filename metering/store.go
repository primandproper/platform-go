package metering

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/database"
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
	//
	// A period nothing has been recorded into holds a floor rather than a
	// reading — a second below PeriodStart, which is what makes the first record
	// in the window strictly newer than what the row holds on every dialect. See
	// metering/internal/queries.
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
//
// # The transaction is the caller's, except where there is no caller
//
// Record and Consume take a database.Tx, and Total takes the wider
// database.SQLQueryExecutor. That is the module's store convention rather than
// anything this package invented, and here it is what the package is for: usage
// recorded in the transaction that produced the work means a rolled-back
// operation does not bill for itself. A caller with genuinely nothing to join
// opens a transaction with Client.WithTransaction and passes the Tx it is
// handed; there is deliberately no WithTransaction on this interface, because a
// store that hands out transactions is a second way in for a caller who should
// be looking at their own client.
//
// The other four — ClaimFlushable, MarkFlushed, ReleaseFlush and ReapEvents —
// take no executor at all, and the asymmetry is deliberate rather than an
// oversight. They are the flush loop and the reaper servicing themselves: there
// is no consumer request behind them and so no transaction of anybody's to join,
// and the correctness the flush protocol depends on is that the claim commits
// *before* the provider is posted to, which a caller holding the transaction
// open across that round trip would destroy. Each says so on its own doc.
//
// A Store that is not a SQL store still takes these types. That is the cost of
// the seam being one signature rather than one per backend, and it is a small
// one: an implementation with no transaction of its own ignores the executor,
// while an application keeping its usage in the same database as the work that
// incurred it — the case this package is written for — gets the guarantee from
// the type.
type Store interface {
	// Record durably ingests usage in the caller's transaction, deduping on
	// idempotency key and folding each new record into its period's total.
	//
	// It exists for the call site where the usage and the work are the same
	// fact: a row inserted and the storage it consumes, a message sent and the
	// credit it spends. Recording those separately means a crash between them
	// leaves usage counted for work that rolled back, or work committed that
	// nobody was billed for.
	//
	// It is not atomic across entries in the sense RecordResult describes — an
	// entry whose idempotency key has been seen is skipped rather than failing
	// the batch. It is entirely atomic in the sense the transaction describes:
	// an error returned from here is an error the caller unwinds, and nothing
	// this call wrote survives it.
	Record(ctx context.Context, tx database.Tx, entries []Entry, at time.Time) (RecordResult, error)

	// Total reads one subject's total for a meter and period. It returns a zero
	// Total, and no error, for a period nothing has been recorded against — an
	// absent row means no usage, which is a number rather than a missing value.
	//
	// It takes the wider executor so that one method serves both of its callers.
	// A dashboard or a quota check holding no transaction passes Client.Reader();
	// a caller that has just recorded usage passes the Tx it recorded in, and
	// reads a total that includes what that transaction has not yet committed.
	Total(ctx context.Context, q database.SQLQueryExecutor, subject, meter string, bounds Bounds) (*Total, error)

	// Consume atomically decides whether entry may be recorded against a limit,
	// records it if so, and returns the decision, all in the caller's
	// transaction.
	//
	// The limit and behavior are passed in rather than looked up, because whose
	// limit applies is a QuotaSource's answer and may differ per subject.
	//
	// The transaction is the caller's for the same reason Record's is, and the
	// stakes are higher: a nil error here is permission to do the work, and work
	// that commits separately from the permission is work that can be done
	// without being counted, or counted without being done. Write the work in
	// this transaction.
	//
	// The consequence is that the row lock this takes is held until the caller
	// commits rather than until the store does, so every other consumer of that
	// subject's meter waits behind whatever else the transaction is doing. That
	// is the price of an exact reservation, and it is the reason Check exists:
	// the cheap path takes no lock at all.
	Consume(
		ctx context.Context,
		tx database.Tx,
		entry Entry,
		limit int64,
		behavior QuotaBehavior,
		at time.Time,
	) (*Decision, error)

	// ClaimFlushable leases the next batch of totals with usage the provider has
	// not been told about, incrementing their attempt counts.
	//
	// It takes no executor. A lease exists so that the provider round trip that
	// follows it happens outside a transaction — the claim has to be committed
	// and visible to every other flusher before the first byte goes out, or two
	// flushers post the same delta. A caller supplying a transaction would be
	// choosing when that commit happens, which is the one thing the protocol
	// cannot let them choose. An implementation runs this on its own connection,
	// and the select and the leases within it are one transaction of its own.
	ClaimFlushable(ctx context.Context, now time.Time, limit, maxAttempts int, leaseUntil time.Time) ([]*Total, error)

	// MarkFlushed records a successful post: the flushed quantity advances to
	// what was posted and the sequence increments, both in one statement.
	//
	// It is guarded on the sequence the flusher read, so a flusher whose lease
	// lapsed while it was posting cannot advance a sequence a second flusher has
	// already moved. Losing that race is how the same delta gets posted twice
	// under two different keys, which no idempotency key can undo.
	//
	// It takes no executor, and the guard is why. What settles this row is the
	// flush sequence read at claim time matching the one in the row now — a
	// comparison whose whole value is that it is evaluated against committed
	// state by a single statement. There is no caller transaction for it to
	// join: the flusher is servicing itself, and the thing it would be joining
	// is a network call to a billing provider.
	MarkFlushed(ctx context.Context, total *Total, flushed int64, at time.Time) error

	// ReleaseFlush returns a total to the flushable set after a failed post,
	// recording why and when it may be retried.
	//
	// It takes no executor, for MarkFlushed's reason: it is the other half of
	// the same guarded settlement, reached down the failure path of the same
	// loop.
	ReleaseFlush(ctx context.Context, total *Total, lastErr string, nextFlush time.Time) error

	// ReapEvents deletes usage event rows recorded at or before horizon, up to
	// limit rows, leaving the totals they were folded into untouched.
	//
	// The boundary is inclusive on the doomed side, which is the reading that
	// leaves no instant at which a row is neither past the horizon nor short of
	// it — and it is the statement's, so the argument is a horizon rather than a
	// "before".
	//
	// It deletes only events whose period has been fully flushed. An event row
	// removed while its period still owes the provider usage would take the
	// evidence for an invoice line with it, and would let a redelivery of that
	// same event be counted a second time.
	//
	// It takes no executor. Retention spans every subject and answers no
	// consumer read — it returns a count, not rows — and it runs from a
	// scheduler tick rather than from anybody's request, so there is no
	// transaction for it to join and no caller who would want it in theirs.
	ReapEvents(ctx context.Context, horizon time.Time, limit int) (int64, error)
}
