package dataprivacy

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/filtering"
)

// Store is the persistence seam for the request state machine.
//
// This package ships a SQL implementation (NewSQLStore) together with the DDL
// it needs (dataprivacy/migrations), so adopting it does not mean writing this.
// The interface exists because the state machine and its storage are genuinely
// separable, and an application with its own schema conventions should not have
// to fork the package to keep them.
//
// Every transition method is a conditional write rather than a read-then-write.
// Two workers can claim, a sweeper can expire, and a subject can cancel, all at
// the same instant; a store that read the row, decided, and wrote it back would
// resolve those races by whichever transaction was slower. The predicates are
// in the queries for that reason, and a transition that matched nothing returns
// an error rather than silently succeeding.
type Store interface {
	// Save inserts a new request using the caller's executor. It does not
	// update: a request row's history is the thing being recorded, and an upsert
	// here would let a resubmission quietly overwrite the timestamp the
	// statutory clock runs from.
	//
	// It takes an executor for the same reason audit.Recorder.Record does. "Who
	// asked for this person's data" is itself an auditable event, and an audit
	// entry that can commit while the request it describes rolls back — or the
	// reverse — is not a record of anything.
	Save(ctx context.Context, q database.SQLQueryExecutor, req *Request) error

	// Get reads one request. It returns an error wrapping ErrRequestNotFound
	// when there is no such request.
	Get(ctx context.Context, requestID string) (*Request, error)

	// List pages through a subject's requests, ordered by ID in the direction
	// the filter's SortBy asks for.
	List(ctx context.Context, subject Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Request], error)

	// Transition moves a request from any of the `from` statuses to `to` using
	// the caller's executor, returning the updated request. It returns an error
	// wrapping ErrRequestNotFound when no row matched — which covers both "no
	// such request" and "the request was not in a state this transition applies
	// to", so callers wrap it into whichever of the two their API means.
	//
	// operationID is recorded alongside the new status when it is non-empty,
	// because the one transition that sets it — a confirmation, which starts the
	// operation as it moves the row — must not be able to commit the status
	// without the pointer to the thing now doing the work.
	Transition(
		ctx context.Context,
		q database.SQLQueryExecutor,
		requestID string,
		from []Status,
		to Status,
		operationID string,
		at time.Time,
	) (*Request, error)

	// CompleteExport records a fulfilled export using the caller's executor: its
	// artifact, that artifact's expiry, and any per-section failures.
	CompleteExport(ctx context.Context, q database.SQLQueryExecutor, req *Request, at time.Time) error

	// WithTransaction runs fn against the store's database.
	//
	// It is on this interface because an erasure has to be atomic across
	// domains and with its own bookkeeping: every registered Eraser and the
	// request's completion share one transaction, so a subject is never left
	// half-erased across eleven domains because the ninth failed. A Store that
	// is not backed by the same database as the erasers cannot offer that, and
	// should refuse erasure rather than pretend.
	WithTransaction(ctx context.Context, fn func(q database.SQLQueryExecutor) error) error

	// CompleteErasure records a fulfilled erasure using the caller's executor,
	// so it commits with the deletions it describes.
	CompleteErasure(ctx context.Context, q database.SQLQueryExecutor, req *Request, at time.Time) error

	// MarkKeyShredded records that the subject's data key was destroyed, on its
	// own and before the erasure it belongs to has finished.
	//
	// It is separate from CompleteErasure because the destruction is separate.
	// It is irreversible, it happens before any row is deleted, and a request
	// that then exhausts its attempts has still destroyed the key — so writing
	// it only at completion would leave the one fact about an erasure that
	// nothing else can reconstruct recorded nowhere.
	//
	// It is idempotent. A retried erasure re-shreds, gets the original
	// destruction time back, and must not overwrite the record with a later one.
	MarkKeyShredded(ctx context.Context, requestID string, at time.Time) error

	// Fail moves an in-progress request to StatusFailed, recording why, and
	// reports whether it moved anything.
	//
	// It is called only on an operation's final attempt — see
	// operations.Attempt — because that is the only moment at which "this
	// request will not be fulfilled" is a true thing to write. Every earlier
	// failure leaves the row in StatusInProgress, which is what it is: the
	// operation is going to try again.
	//
	// False with a nil error means the row was not in StatusInProgress: it was
	// cancelled, or completed by a duplicate execution that got there first. It
	// is not an error, because in both of those the row already says something
	// truer than "failed" — but the caller has to know, because telling a
	// subject their request failed when it was cancelled is worse than telling
	// them nothing.
	Fail(ctx context.Context, requestID, lastErr string, at time.Time) (bool, error)

	// ExpiringArtifacts returns completed exports whose artifacts are due for
	// deletion. The sweeper deletes each object before calling MarkExpired, so
	// this deliberately returns the requests rather than expiring them in bulk:
	// a row marked expired while its object survived is a file nobody is
	// looking for any more and nobody will delete.
	ExpiringArtifacts(ctx context.Context, now time.Time, limit int) ([]*Request, error)

	// MarkExpired clears a request's artifact reference and moves it to
	// StatusExpired, once the object itself is gone.
	MarkExpired(ctx context.Context, requestID string, at time.Time) error

	// LapseUnconfirmed cancels erasures whose confirmation window has passed,
	// returning how many were cancelled.
	LapseUnconfirmed(ctx context.Context, now time.Time, limit int) (int64, error)

	// CountOverdue counts unfulfilled requests past their statutory deadline,
	// by request type, for the sweeper's gauge.
	CountOverdue(ctx context.Context, now time.Time) (map[RequestType]int64, error)

	// Reap deletes terminal request records completed before the given time, up
	// to limit rows.
	//
	// Records of privacy requests are themselves personal data, and keeping
	// them forever is the mistake this package would otherwise make on every
	// consumer's behalf. What it does not do is delete a request whose artifact
	// still exists — see the retention discussion in the package docs.
	Reap(ctx context.Context, before time.Time, limit int) (int64, error)
}
