package saga

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
)

// ListScope narrows a listing. A nil *ListScope, or one with both fields
// empty, lists everything.
type ListScope struct {
	// Definition narrows to one definition. Empty means all of them.
	Definition string `json:"definition,omitempty"`

	// Statuses narrows to a set of statuses. Empty means all of them, and
	// []Status{StatusStuck} is the query an operator actually runs.
	Statuses []Status `json:"statuses,omitempty"`
}

// Store is the persistence seam for the instance state machine.
//
// This package ships a SQL implementation (NewSQLStore) together with the DDL
// it needs (saga/migrations), so adopting it does not mean writing this. The
// interface exists because the state machine and its storage are genuinely
// separable, and an application with its own schema conventions should not have
// to fork the package to keep them.
//
// It moves Record — an instance whose state is still encoded bytes — rather
// than a generic Instance[T]. See the definition type's commentary for why the
// erasure happens at the registry and not here.
//
// Every transition method is a conditional write rather than a read-then-write.
// A worker can be advancing an instance while its lease expires and a second
// worker claims it; a store that read the row, decided, and wrote it back would
// resolve that by whichever transaction was slower, and the loser would
// overwrite a cursor that had already moved. The predicates are in the queries
// for that reason, and a write that matched nothing returns an error rather
// than silently succeeding.
//
// # Two of these take an executor and six take nothing
//
// The convention everywhere else in this module is that a store write reads
// (ctx, tx database.Tx, scope tenancy.Scope, ...) and a store read reads
// (ctx, q database.SQLQueryExecutor, scope tenancy.Scope, ...). Almost nothing
// here does, and the deviation is a ruling rather than an oversight. Each
// method says which half it is in; this is the argument the clauses are short
// for.
//
// Save and Advance take a database.Tx, which only Client.WithTransaction
// produces, so their signatures are a compile-time claim that the caller is
// already inside one. That is the whole point for Save — starting a saga has to
// commit with whatever the caller wrote to decide to start it — and for Advance
// it is the lifecycle event, which must not survive an advance that rolled
// back.
//
// Claim, Reschedule, Release and Requeue take no executor at all, and run on
// the handle the implementation was built with. They are the component
// servicing itself: a worker on a timer claiming what is due, reporting what it
// did with it, and handing back what it could not finish. There is no consumer
// request behind any of them and so no transaction of anybody's to join, and
// the lease protocol's correctness is that a claim commits before a step runs —
// a caller supplying a transaction would be choosing when that commit happens,
// which is the one thing the protocol cannot let them choose. It is the
// carve-out webhooks.Store names its seven for and metering's flush protocol
// takes, and it is the same narrowness: a worker on a timer, not any method
// this package finds convenient to keep to itself.
//
// Get and List take none either, and they are the two worth arguing about,
// because a person does read them — an operator listing what is stuck, a
// console showing one saga's position. What closes it is that there is nothing
// here for a wider executor to make visible. Save is the only write of these
// rows a consumer's transaction ever contains, and Runner.StartInTransaction
// hands back the Instance it just wrote rather than making the caller go and
// find it. The read-your-own-writes gap the executor argument exists to close
// does not open in this package.
//
// # No scope, anywhere, and there will not be one
//
// A saga is a workflow instance, not consumer data. There is no tenancy column
// in this package and no argument for adding one: a consumer never holds a saga
// row on a subject's behalf — it holds the order, the booking, the refund, and
// the saga is how those came to be written. A claim confined to a tenant would
// need a list of tenants nothing maintains, which is why webhooks.Store.Claim
// spans every scope too, and a read confined to one would be filtering on a
// column that does not exist.
//
// What a saga's state carries about a person is whatever the application
// encoded into it, and keeping that out is the application's job rather than a
// column's. See the package documentation on retention.
//
// # There is no WithTransaction
//
// One way in, and it is database.Client's own: a caller with nothing to join
// writes client.WithTransaction(ctx, fn) and hands the Tx to
// Runner.StartInTransaction, and a caller with something to join is already
// holding one. This interface carried a WithTransaction of its own once, and it
// came off for the reason operations.Store's did — a per-store wrapper is a
// second exported name for one behavior, and a Store is not where a transaction
// comes from. NewRunner and NewWorker take a database.Client for that reason:
// what used to be reached for through the store is now passed to the two things
// that actually needed it.
type Store interface {
	// Save inserts a new instance using the caller's transaction. It does not
	// update: an instance ID is minted per Start, and an upsert here would let
	// a retried Start silently rewind a saga that was already halfway through.
	//
	// It takes a transaction so that starting a saga commits with whatever the
	// caller wrote to decide to start it. A saga that exists only after the
	// caller's transaction has committed is one that does not exist at all if
	// the process dies in the gap.
	//
	// nextAttempt is when the instance first becomes claimable, which is now
	// unless the first step carries a Delay.
	Save(ctx context.Context, q database.Tx, inst *Record, nextAttempt time.Time) error

	// Get reads one instance. It returns an error wrapping ErrInstanceNotFound
	// when there is no such instance.
	//
	// It takes no executor: the only write of this row a consumer's transaction
	// ever contains is Save, and the caller who made it already holds what it
	// wrote. It takes no scope because a saga belongs to no tenant.
	Get(ctx context.Context, instanceID string) (*Record, error)

	// List pages through instances, ordered by ID in the direction the filter's
	// SortBy asks for.
	//
	// It takes neither an executor nor a scope, for Get's reasons. It is the
	// operator's query — []Status{StatusStuck} is the one actually run — and an
	// operator asking what is stuck is asking about the deployment rather than
	// about a tenant.
	List(
		ctx context.Context,
		scope *ListScope,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Record], error)

	// Claim leases the next batch of instances due to be advanced, moving their
	// claimed_until forward and incrementing their attempt counts.
	//
	// The attempt count is incremented here rather than on failure, so a step
	// that reliably kills its worker — a nil map access in somebody's payment
	// client — exhausts its budget and compensates instead of being reclaimed
	// forever.
	//
	// It takes no executor: the lease has to be committed before the step runs,
	// or a second worker claims the same instance while the first is mid-step,
	// and a caller supplying a transaction would be choosing when that commit
	// happens. It spans the whole table, because there is no scope to narrow it
	// to and one worker drains one deployment.
	Claim(ctx context.Context, now time.Time, limit int, leaseUntil time.Time) ([]*Record, error)

	// Advance records that the cursor moved: the instance's status, step,
	// state, error, and when it next becomes claimable. Attempts are reset to
	// zero, because the step they counted is behind us either way.
	//
	// It takes a transaction so the position and whatever lifecycle event
	// describes it commit together — an event that survives a rolled-back
	// advance describes something that did not happen. It is the one machinery
	// write here that takes one, and the Worker opens it on its own client
	// rather than joining anybody: see Worker.persist.
	//
	// A terminal status also drops the lease: nothing will claim the instance
	// again, and a claimed_until left in the future would keep it out of the
	// claim index for no reason.
	Advance(ctx context.Context, q database.Tx, inst *Record, nextAttempt, at time.Time) error

	// Reschedule records a step that failed and will be tried again: the
	// attempt count, the rendered error, and when. It drops the lease.
	//
	// Like Claim it takes no executor: it releases a lease Claim committed, and
	// the step it is reporting on has already run.
	Reschedule(ctx context.Context, instanceID string, attempts int, nextAttempt time.Time, lastErr string, at time.Time) error

	// Release drops a lease without changing anything else, so a worker that
	// ran out of time mid-saga hands the instance back rather than holding it
	// until the lease expires.
	//
	// It takes no executor for Reschedule's reason: it is the other half of the
	// same lease, handed back down the one path that wrote nothing else.
	Release(ctx context.Context, instanceID string, at time.Time) error

	// Requeue moves an instance from any of the `from` statuses to `to` and
	// makes it immediately claimable, returning the updated instance. It is how
	// Resume re-drives a stuck saga.
	//
	// It returns an error wrapping ErrInstanceNotFound when no row matched,
	// which covers both "no such instance" and "not in a status this applies
	// to" — callers wrap it into whichever of the two their API means.
	//
	// It takes no executor because it writes the same column Claim writes, and
	// a row made claimable inside a caller's transaction is a row no worker can
	// see until that transaction ends. An operator re-driving a stuck saga is
	// asking the queue to move, not asking for a row that commits with
	// something else of theirs.
	Requeue(ctx context.Context, instanceID string, from []Status, to Status, at time.Time) (*Record, error)
}
