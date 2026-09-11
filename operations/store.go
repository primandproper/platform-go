package operations

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
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
//
// # Which methods take a scope, and which take neither
//
// The four a consumer calls — Insert, Get, GetMany and List — take a
// tenancy.Scope, and the three reads take an executor beside it. There is no
// read here that omits the scope, because the caller who reaches for the
// unscoped one is the caller who has not thought about tenancy, and what an
// operation holds is the status of somebody's export.
//
// The other seven — Begin, Progress, Finish, Release, RequestCancel, Stranded
// and Reap — take neither, and each says so on itself. They are the component
// servicing itself: a worker on a timer, holding an id a dispatch handed it,
// running on the handle the store was built with. It is the carve-out
// webhooks.Store names its seven for and metering's flush protocol takes, and it
// is the same narrowness — a worker on a timer, not any method this package
// finds convenient to keep to itself.
//
// There is no WithTransaction. One way in, and it is database.Client's own: a
// caller with nothing to join writes client.WithTransaction(ctx, fn) and hands
// the tx to Insert.
type Store interface {
	// Insert records a new operation in StatePending using the caller's
	// transaction, under scope.
	//
	// It does not upsert: an operation ID is minted per Start, and an upsert here
	// would let a retried Start rewind an operation that was already halfway
	// through.
	//
	// It takes a transaction so that starting an operation commits with whatever
	// the caller wrote to decide to start it, and it returns the row it wrote —
	// server timestamps and all — because a caller inside an uncommitted
	// transaction has no other way to read it back.
	//
	// The scope is an argument even though op carries one, for the reason
	// comments.Store.CreateComment takes it beside a Comment that already names
	// one: a scope read off a struct somebody assembled elsewhere is the
	// derivation the column rule exists to rule out. An operation whose Owner
	// disagrees with the argument is refused with ErrScopeMismatch; one that
	// names nobody adopts it.
	//
	// It returns an error wrapping ErrDuplicateOperation when the ID is already
	// taken, without disturbing the surrounding transaction. That is the
	// idempotency seam WithID exists for.
	Insert(ctx context.Context, tx database.Tx, scope tenancy.Scope, op *Operation) (*Operation, error)

	// Get reads one operation belonging to scope. It returns an error wrapping
	// ErrOperationNotFound when there is no such operation.
	//
	// An operation that exists under another scope is reported as absent rather
	// than as a permission failure, and the two being one answer is the point: a
	// 403 for an operation that exists and a 404 for one that does not is an
	// oracle telling whoever is guessing IDs which of their guesses are real.
	//
	// The executor is the wider type on purpose. A database.Tx satisfies it, so
	// a caller inside a transaction reads that transaction's own uncommitted
	// writes and a caller outside one passes Client.Reader().
	Get(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		id string,
	) (*Operation, error)

	// GetMany reads a set of operations belonging to scope in one statement,
	// skipping IDs that are not in the table rather than failing.
	//
	// It is the watch path's read: a payload-free notification says only that
	// something changed, so the watcher re-reads everything it is following. An
	// operation that has been reaped out from under a subscriber is a gap, not
	// an error — the subscriber is told the stream is over by other means.
	//
	// One scope beside a set of ids rather than one scope per id, which is why
	// Watcher holds a scope per subscription and re-reads a scope's ids per
	// statement. An id in another scope is one of the gaps.
	GetMany(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		ids []string,
	) ([]*Operation, error)

	// List pages through the operations scope owns, ordered by ID in the
	// direction the filter's SortBy asks for and narrowed further by listScope.
	//
	// listScope may be nil, and every field on it may be left off; the scope may
	// not, which is the whole difference between the two arguments.
	List(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		listScope *ListScope,
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
	//
	// It takes neither an executor nor a scope. The claim is a Worker's, made on
	// an id a dispatch handed it across every tenant the queue serves, and it
	// commits on its own before the Runner produces a single effect — so a
	// caller supplying a transaction would be choosing when a lease is taken,
	// which is the one thing this protocol cannot let them choose.
	Begin(ctx context.Context, id string, attempts int, lease time.Duration) (*Operation, error)

	// Progress records buffered progress, extends the lease, and reports what
	// the row had to say back. See Ack.
	//
	// It takes neither, for Begin's reason: it is the running worker's own flush,
	// on the operation it is holding, and a lease extension that waited for
	// somebody else's transaction to commit is a lease that has already lapsed.
	Progress(ctx context.Context, id string, progress Progress, lease time.Duration) (Ack, error)

	// Finish writes a terminal state, dropping the lease.
	//
	// unitsAllDone raises units_done to the declared total, for a success that
	// finished every unit without reporting the last one.
	//
	// It takes neither, for Begin's reason. The outcome is the worker's to
	// record and it must land whether or not anything else the process was doing
	// commits — an operation whose result was rolled back with somebody else's
	// transaction is one that runs again.
	Finish(ctx context.Context, id string, state State, result *Result, opErr *Error, unitsAllDone bool) error

	// Release hands a running operation back to StatePending for another
	// attempt, recording the failure that caused it.
	//
	// It takes neither, for Finish's reason: it is the other half of the same
	// worker's outcome, and a release that did not commit is a lease held open
	// for work nobody else may claim and this worker will not resume.
	Release(ctx context.Context, id string, opErr *Error) error

	// RequestCancel flags an operation for cancellation, cancelling it outright
	// if it has not started, and returns it as it now stands.
	//
	// Cancelling a terminal operation is not an error: the caller wanted it not
	// running, and it is not running.
	//
	// It takes neither, and it is the one of the seven a consumer reaches
	// through — Service.Cancel — so the omission is worth stating rather than
	// only being true. The write is a conditional transition on the id and it
	// reads the row back on the same handle; what confines it to a tenant is the
	// scoped read the caller makes first, which is what Service.Cancel's own
	// callers do and what operations/http does before it. A consumer holding
	// this interface directly owns that read.
	RequestCancel(ctx context.Context, id string) (*Operation, error)

	// Stranded reads active operations that nothing is going to pick up: pending
	// ones older than grace, and running ones whose lease lapsed that long ago.
	//
	// It takes neither, and here the scope is not merely absent but wrong: a
	// sweep that recovered one tenant's operations would leave every other
	// tenant's stranded. It is a fleet-wide read on a timer, and Service.Recover
	// is what runs it.
	Stranded(ctx context.Context, grace time.Duration, limit int) ([]*Operation, error)

	// Reap deletes terminal operations finished longer than retention ago,
	// returning how many rows went.
	//
	// It takes neither, for Stranded's reason: a retention policy bounded by
	// scope is one that only runs for whoever asked for it.
	Reap(ctx context.Context, retention time.Duration, limit int) (int64, error)
}
