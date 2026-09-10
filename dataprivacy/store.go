package dataprivacy

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
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
// # Executors and scopes
//
// Every consumer-facing write takes the caller's database.Tx and every
// consumer-facing read takes a database.SQLQueryExecutor, so one read serves
// both a caller holding Client.Reader() and a caller inside a transaction —
// and the caller who has just written a request can read it back. Every read
// that selects rows takes the scope it selects by as an argument rather than
// off a struct, so a call that did not decide which tenant it was for does not
// compile into one that quietly means all of them.
//
// There is no WithTransaction here, and it is worth saying why it went. It used
// to be on this interface, arguing that an erasure has to be atomic across
// domains and with its own bookkeeping. That is true and it is not a method
// this store owes: database.Client.WithTransaction already provides it, and one
// way in is the module's rule. What the argument was really recording is that
// every registered Eraser and the request's completion must share one
// transaction — a statement about how Fulfiller is wired, which is where it now
// lives.
//
// # Why the writes take no scope
//
// Every other store in this module binds one on every write, entity-carrying
// creates included: comments.Store.CreateComment takes the scope beside a
// Comment that already names one, and issuereports.Store.TransitionReport takes
// it beside the id it moves. Save, Confirm, Cancel, CompleteExport and
// CompleteErasure take none, and what makes them the exception is the column
// rather than an exemption anybody claimed.
//
// A tenancy column elsewhere holds an owner or the empty identifier Global
// stores as, and the zero Scope is not among the values it may hold — which is
// what gives "the entity names none, so adopt the argument" something to key
// on. This confinement is nullable. The zero Scope is a request that named no
// tenant, which is an answer the column really stores, and Global is refused
// outright; so there is no unset state here to tell an unconfined one from. A
// scope argument beside Request.Scope could refuse a disagreement between the
// two and could adopt nothing, which leaves the caller passing the confinement
// twice and keeping the copies in step — the derivation the read side has just
// stopped making, handed back to whoever calls.
//
// What guards the transitions is the predicate they already carry. Confirm,
// Cancel and the two completions are conditional writes matching on the id and
// on the status the request has to be in, which is the guard that matters for a
// row two workers and a sweeper may all be reaching for. The confinement is
// guarded a layer up: StoreService reads the request under the caller's scope
// before it transitions anything, and a request outside that scope is absent
// there rather than forbidden. A consumer holding this interface directly
// rather than through StoreService owns that read, and owes itself the same
// one.
//
// # The seven that take neither
//
// MarkKeyShredded, Fail, ExpiringArtifacts, MarkExpired, LapseUnconfirmed,
// CountOverdue and Reap run on the handle the store was built with. They are
// this component servicing itself — a sweeper on a timer, and the runner
// recording that an operation's last attempt is spent — rather than answering a
// consumer read, so there is no caller whose transaction they could join and no
// tenant whose question they are answering. A sweep that took a caller's
// transaction would hold every tenant's rows inside somebody else's unit of
// work; one that took a scope would have to be run once per tenant to do a job
// that is defined across all of them. Each says so on itself.
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
	//
	// It takes no scope, which puts it at odds with every other create in the
	// module and is argued above under "Why the writes take no scope": the
	// confinement is Request.Scope, and a nullable one has no unset state for a
	// scope argument to adopt. tenancy.Global is refused with
	// ErrGlobalRequestScope, for the reason that sentinel gives.
	Save(ctx context.Context, tx database.Tx, req *Request) error

	// Get reads one request, through the caller's executor.
	//
	// It returns an error wrapping ErrRequestNotFound when there is no such
	// request, and the same error when the request exists outside the scope
	// named. Those are one answer on purpose: a caller who may not see a
	// request learns nothing from the difference between "no such request" and
	// "not yours", and the second wording is how an opaque identifier becomes
	// an oracle.
	//
	// See List for what the scope pointer's three readings are; they are the
	// same three here.
	Get(ctx context.Context, q database.SQLQueryExecutor, scope *tenancy.Scope, requestID string) (*Request, error)

	// List pages through a subject's requests, ordered by ID in the direction
	// the filter's SortBy asks for, under the rest of the filter's window.
	//
	// # The scope's three readings
	//
	// A nil scope matches every confinement rather than only the unconfined
	// requests. A subject asking what has been requested in their name means
	// all of it, and a listing that silently omitted the confined requests
	// would be the wrong answer to the one question this method exists to
	// answer.
	//
	// A non-nil scope naming an owner matches that confinement and no other.
	//
	// A non-nil scope naming nobody — the zero tenancy.Scope — is a caller
	// whose own lookup came back empty, and it is refused with
	// tenancy.ErrNoScope rather than widened into the first reading. That is
	// the pointer's whole reason for being: Scope.known separates the global
	// scope from a caller who never decided, and it does not separate either of
	// those from "do not narrow at all". audit.Query.Scope carries the same
	// three readings in the same shape, and for the same reason.
	//
	// tenancy.Global is not a confinement any request holds — Save refuses it —
	// so a scope naming it matches nothing rather than matching the unconfined
	// requests.
	List(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope *tenancy.Scope,
		subject Subject,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Request], error)

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
	Confirm(ctx context.Context, tx database.Tx, requestID, operationID string) (*Request, error)

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
	Cancel(ctx context.Context, tx database.Tx, requestID string, from Status, at time.Time) (*Request, error)

	// CompleteExport records a fulfilled export using the caller's transaction: its
	// artifact, that artifact's expiry, and any per-section failures.
	//
	// It returns an error wrapping ErrUnexpiringArtifact when the request names
	// an artifact and its ExpiresAt is zero. The expiry is what the artifact
	// sweep matches on, so a completion without one would write a row pointing
	// at a packaged copy of everything held about somebody that no sweep will
	// ever visit. The expiry is refused rather than defaulted, because how long
	// an export stays fetchable is the caller's policy and not this store's.
	CompleteExport(ctx context.Context, tx database.Tx, req *Request, at time.Time) error

	// CompleteErasure records a fulfilled erasure using the caller's transaction,
	// so it commits with the deletions it describes.
	CompleteErasure(ctx context.Context, tx database.Tx, req *Request, at time.Time) error

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
	//
	// It takes neither executor nor scope. The shred happens before the erasure
	// transaction opens and has to survive that transaction rolling back — the
	// key is gone either way, and the one fact nothing else can reconstruct is
	// when — so joining the caller's transaction is the one thing it must not
	// do. The request it stamps is already identified by id.
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
	//
	// It takes neither executor nor scope. It is the runner recording that an
	// operation is out of attempts, on its own handle and after whatever
	// transaction the attempt held has gone; the row it moves is named by id.
	Fail(ctx context.Context, requestID, lastErr string, at time.Time) (bool, error)

	// ExpiringArtifacts returns completed exports whose artifacts are due for
	// deletion. The sweeper deletes each object before calling MarkExpired, so
	// this deliberately returns the requests rather than expiring them in bulk:
	// a row marked expired while its object survived is a file nobody is
	// looking for any more and nobody will delete.
	//
	// It takes neither executor nor scope. It is the artifact sweep asking what
	// is due across every tenant, on the store's own handle: an expiry is a
	// deadline the clock reached, not a question anybody asked about their own
	// data.
	ExpiringArtifacts(ctx context.Context, now time.Time, limit int) ([]*Request, error)

	// MarkExpired clears a request's artifact reference and moves it to
	// StatusExpired, once the object itself is gone.
	//
	// It takes neither executor nor scope, as the sweep that calls it does not.
	// It runs after the object has been deleted, so it must commit on its own:
	// a transaction the caller could still roll back would leave a row pointing
	// at a file that no longer exists.
	MarkExpired(ctx context.Context, requestID string, at time.Time) error

	// LapseUnconfirmed cancels erasures whose confirmation window has passed,
	// returning how many were cancelled.
	//
	// It takes neither executor nor scope. It is one bounded write across every
	// tenant on a timer, and a lapse is the absence of a confirmation rather
	// than anybody's request.
	LapseUnconfirmed(ctx context.Context, now time.Time, limit int) (int64, error)

	// CountOverdue counts unfulfilled requests past their statutory deadline,
	// by request type, for the sweeper's gauge.
	//
	// Every type is in the result whether or not any request of that type is
	// overdue, so a gauge that was reporting three overdue exports actively
	// drops to zero when they are served rather than holding a stale reading.
	//
	// It takes neither executor nor scope. It feeds a process-level gauge, whose
	// reading is "how far behind is this deployment" — a number defined across
	// every tenant, which a per-scope count could not produce without being run
	// once per tenant and summed.
	CountOverdue(ctx context.Context, now time.Time) (map[RequestType]int64, error)

	// Reap deletes terminal request records completed before the given time, up
	// to limit rows.
	//
	// Records of privacy requests are themselves personal data, and keeping
	// them forever is the mistake this package would otherwise make on every
	// consumer's behalf. What it does not do is delete a request whose artifact
	// still exists — see the retention discussion in the package docs.
	//
	// It takes neither executor nor scope. Retention is the deployment's policy
	// applied to every tenant's records on a timer, not a deletion any subject
	// asked for; the erasure they did ask for is CompleteErasure, which takes
	// the caller's transaction.
	Reap(ctx context.Context, before time.Time, limit int) (int64, error)
}
