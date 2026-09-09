package dataprivacy

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/filtering"
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
//
// The two transitions are named rather than parameterized, and the difference
// between them is one column rather than the source status. A confirmation
// records the operation now doing the work; a cancellation must not touch that
// column, because blanking it would lose the pointer to an operation that is
// still running. A single method taking a destination status would have to
// decide which of those to do from the shape of its arguments.
type Store interface {
	// Save inserts a new request using the caller's transaction. It does not
	// update: a request row's history is the thing being recorded, and an upsert
	// here would let a resubmission quietly overwrite the timestamp the
	// statutory clock runs from.
	//
	// A request saved in a terminal status carries its CompletedAt, which is
	// what CountOverdue and Reap read terminality off — see that field.
	//
	// A request saved with an ArtifactRef must carry an ExpiresAt, and one
	// saved without one is refused with the error wrapping
	// ErrUnexpiringArtifact that CompleteExport returns. Insert and completion
	// are the only two statements that write an artifact reference, so guarding
	// both is what makes the invariant hold for the table rather than for one
	// code path.
	//
	// It takes a transaction for the same reason audit.Recorder.Record does. "Who
	// asked for this person's data" is itself an auditable event, and an audit
	// entry that can commit while the request it describes rolls back — or the
	// reverse — is not a record of anything.
	Save(ctx context.Context, q database.Tx, req *Request) error

	// Get reads one request. It returns an error wrapping ErrRequestNotFound
	// when there is no such request.
	Get(ctx context.Context, requestID string) (*Request, error)

	// List pages through a subject's requests, ordered by ID in the direction
	// the filter's SortBy asks for, under the rest of the filter's window.
	//
	// An empty Subject.Scope matches every scope rather than only the unscoped
	// requests. A subject asking what has been requested in their name means all
	// of it, and a listing that silently omitted the scoped requests would be
	// the wrong answer to the one question this endpoint exists to answer.
	List(ctx context.Context, subject Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Request], error)

	// Confirm moves a request out of StatusAwaitingConfirmation and into
	// StatusInProgress using the caller's transaction, recording the operation
	// that will fulfill it, and returns the updated request.
	//
	// The operation is written by the same statement as the status, because a
	// row that became in-progress without saying what is doing the work is a
	// request nothing is fulfilling and nothing can be asked about.
	//
	// It returns an error wrapping ErrRequestNotFound when no row matched, which
	// covers both "no such request" and "the request was not awaiting
	// confirmation" — a subject clicking confirm twice, or clicking it at the
	// instant the lapse sweep cancelled it. Callers wrap it into whichever of
	// the two their API means.
	Confirm(ctx context.Context, q database.Tx, requestID, operationID string) (*Request, error)

	// Cancel moves a request from the named status to StatusCancelled using the
	// caller's transaction, stamping at as its completion, and returns the
	// updated request.
	//
	// The source status is the caller's because there are two of them and they
	// mean different things: an unconfirmed erasure the subject withdrew, and an
	// in-flight one whose runner was told to stop. Binding it as a guard is what
	// keeps a cancellation from moving a request that has since gone somewhere
	// else.
	//
	// It leaves the operation reference alone. A request cancelled while its
	// operation is still running is a row that has to keep pointing at the thing
	// being stopped, which is the whole difference between this statement and
	// Confirm's.
	//
	// It returns an error wrapping ErrRequestNotFound when no row matched, and
	// one wrapping ErrUnknownStatus when from is not a status this package
	// writes.
	Cancel(ctx context.Context, q database.Tx, requestID string, from Status, at time.Time) (*Request, error)

	// CompleteExport records a fulfilled export using the caller's transaction: its
	// artifact, that artifact's expiry, and any per-section failures.
	//
	// It returns an error wrapping ErrUnexpiringArtifact when the request names
	// an artifact and its ExpiresAt is zero. The expiry is what the artifact
	// sweep matches on, so a completion without one would write a row pointing
	// at a packaged copy of everything held about somebody that no sweep will
	// ever visit. The expiry is refused rather than defaulted, because how long
	// an export stays fetchable is the caller's policy and not this store's.
	CompleteExport(ctx context.Context, q database.Tx, req *Request, at time.Time) error

	// WithTransaction runs fn against the store's database.
	//
	// It is on this interface because an erasure has to be atomic across
	// domains and with its own bookkeeping: every registered Eraser and the
	// request's completion share one transaction, so a subject is never left
	// half-erased across eleven domains because the ninth failed. A Store that
	// is not backed by the same database as the erasers cannot offer that, and
	// should refuse erasure rather than pretend.
	WithTransaction(ctx context.Context, fn func(q database.Tx) error) error

	// CompleteErasure records a fulfilled erasure using the caller's transaction,
	// so it commits with the deletions it describes.
	CompleteErasure(ctx context.Context, q database.Tx, req *Request, at time.Time) error

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
	//
	// Every type is in the result whether or not any request of that type is
	// overdue, so a gauge that was reporting three overdue exports actively
	// drops to zero when they are served rather than holding a stale reading.
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
