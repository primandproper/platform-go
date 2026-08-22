package operations

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/filtering"
)

// Ack is what a progress flush learns on its way back.
//
// A flush is the one statement a running operation issues regularly, so it is
// where the two things a Runner needs to be told about arrive: that somebody
// asked it to stop, and that it no longer holds the operation.
type Ack struct {
	// Revision is the row's revision after the flush.
	Revision int64

	// CancelRequested reports that somebody called Cancel.
	CancelRequested bool

	// Held reports whether the flush matched the row at all. False means this
	// worker's lease lapsed and somebody else has the operation — the write did
	// nothing, and the Runner should stop rather than carry on producing effects
	// under an operation it no longer owns.
	Held bool
}

// Store is the persistence seam for the operation row.
//
// This package ships a SQL implementation (NewSQLStore) together with the DDL it
// needs (operations/migrations), so adopting it does not mean writing this. The
// interface exists because the state machine and its storage are genuinely
// separable, and an application with its own schema conventions should not have
// to fork the package to keep them.
//
// Every transition method is a conditional write rather than a read-then-write.
// A worker can be running an operation while its lease expires and a second
// worker begins it; a store that read the row, decided, and wrote it back would
// resolve that by whichever transaction was slower, and the loser would
// overwrite a result that had already been recorded. The predicates are in the
// queries for that reason, and a write that matched nothing says so rather than
// silently succeeding.
type Store interface {
	// Insert records a new operation in StatePending using the caller's
	// executor.
	//
	// It does not upsert: an operation ID is minted per Start, and an upsert here
	// would let a retried Start rewind an operation that was already halfway
	// through.
	//
	// It takes an executor so that starting an operation commits with whatever
	// the caller wrote to decide to start it, and it returns the row it wrote —
	// server timestamps and all — because a caller inside an uncommitted
	// transaction has no other way to read it back.
	//
	// It returns an error wrapping ErrDuplicateOperation when the ID is already
	// taken, without disturbing the surrounding transaction. That is the
	// idempotency seam WithID exists for.
	Insert(ctx context.Context, q database.SQLQueryExecutor, op *Operation) (*Operation, error)

	// Get reads one operation. It returns an error wrapping
	// ErrOperationNotFound when there is no such operation.
	Get(ctx context.Context, id string) (*Operation, error)

	// GetMany reads a set of operations in one statement, skipping IDs that are
	// not in the table rather than failing.
	//
	// It is the watch path's read: a payload-free notification says only that
	// something changed, so the watcher re-reads everything it is following. An
	// operation that has been reaped out from under a subscriber is a gap, not
	// an error — the subscriber is told the stream is over by other means.
	GetMany(ctx context.Context, ids []string) ([]*Operation, error)

	// List pages through operations, ordered by ID in the direction the filter's
	// SortBy asks for.
	List(
		ctx context.Context,
		scope *ListScope,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Operation], error)

	// Begin moves an operation to StateRunning under a lease and returns it as
	// it now stands, request included.
	//
	// It is the guarded transition that makes two workers holding the same
	// dispatch harmless: exactly one of them matches the predicate. It returns
	// an error wrapping ErrOperationNotFound when the operation is gone,
	// terminal, or still leased by somebody else — the three cases in which this
	// worker must not run it, and which the caller distinguishes by reading the
	// row if it cares.
	//
	// attempts is the count the work queue's claim already incremented, written
	// through so there is one attempt counter in the system.
	Begin(ctx context.Context, id string, attempts int, lease time.Duration) (*Operation, error)

	// Progress records buffered progress, extends the lease, and reports what
	// the row had to say back. See Ack.
	Progress(ctx context.Context, id string, progress Progress, lease time.Duration) (Ack, error)

	// Finish writes a terminal state, dropping the lease.
	//
	// unitsAllDone raises units_done to the declared total, for a success that
	// finished every unit without reporting the last one.
	Finish(ctx context.Context, id string, state State, result *Result, opErr *Error, unitsAllDone bool) error

	// Release hands a running operation back to StatePending for another
	// attempt, recording the failure that caused it.
	Release(ctx context.Context, id string, opErr *Error) error

	// RequestCancel flags an operation for cancellation, cancelling it outright
	// if it has not started, and returns it as it now stands.
	//
	// Cancelling a terminal operation is not an error: the caller wanted it not
	// running, and it is not running.
	RequestCancel(ctx context.Context, id string) (*Operation, error)

	// Stranded reads active operations that nothing is going to pick up: pending
	// ones older than grace, and running ones whose lease lapsed that long ago.
	Stranded(ctx context.Context, grace time.Duration, limit int) ([]*Operation, error)

	// Reap deletes terminal operations finished longer than retention ago,
	// returning how many rows went.
	Reap(ctx context.Context, retention time.Duration, limit int) (int64, error)

	// WithTransaction runs fn against the store's database. It is on this
	// interface because Start has to be atomic with the caller's own writes when
	// the caller supplies an executor, and atomic with its own bookkeeping when
	// it does not.
	WithTransaction(ctx context.Context, fn func(q database.SQLQueryExecutor) error) error
}
