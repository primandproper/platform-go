package saga

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/filtering"
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
type Store interface {
	// Save inserts a new instance using the caller's executor. It does not
	// update: an instance ID is minted per Start, and an upsert here would let
	// a retried Start silently rewind a saga that was already halfway through.
	//
	// It takes an executor so that starting a saga commits with whatever the
	// caller wrote to decide to start it. A saga that exists only after the
	// caller's transaction has committed is one that does not exist at all if
	// the process dies in the gap.
	//
	// nextAttempt is when the instance first becomes claimable, which is now
	// unless the first step carries a Delay.
	Save(ctx context.Context, q database.SQLQueryExecutor, inst *Record, nextAttempt time.Time) error

	// Get reads one instance. It returns an error wrapping ErrInstanceNotFound
	// when there is no such instance.
	Get(ctx context.Context, instanceID string) (*Record, error)

	// List pages through instances, ordered by ID in the direction the filter's
	// SortBy asks for.
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
	Claim(ctx context.Context, now time.Time, limit int, leaseUntil time.Time) ([]*Record, error)

	// Advance records that the cursor moved: the instance's status, step,
	// state, error, and when it next becomes claimable. Attempts are reset to
	// zero, because the step they counted is behind us either way.
	//
	// It takes an executor so the position and whatever lifecycle event
	// describes it commit together — an event that survives a rolled-back
	// advance describes something that did not happen.
	//
	// A terminal status also drops the lease: nothing will claim the instance
	// again, and a claimed_until left in the future would keep it out of the
	// claim index for no reason.
	Advance(ctx context.Context, q database.SQLQueryExecutor, inst *Record, nextAttempt time.Time) error

	// Reschedule records a step that failed and will be tried again: the
	// attempt count, the rendered error, and when. It drops the lease.
	Reschedule(ctx context.Context, instanceID string, attempts int, nextAttempt time.Time, lastErr string, at time.Time) error

	// Release drops a lease without changing anything else, so a worker that
	// ran out of time mid-saga hands the instance back rather than holding it
	// until the lease expires.
	Release(ctx context.Context, instanceID string, at time.Time) error

	// Requeue moves an instance from any of the `from` statuses to `to` and
	// makes it immediately claimable, returning the updated instance. It is how
	// Resume re-drives a stuck saga.
	//
	// It returns an error wrapping ErrInstanceNotFound when no row matched,
	// which covers both "no such instance" and "not in a status this applies
	// to" — callers wrap it into whichever of the two their API means.
	Requeue(ctx context.Context, instanceID string, from []Status, to Status, at time.Time) (*Record, error)

	// WithTransaction runs fn against the store's database.
	//
	// It is on this interface because Start has to be atomic with the caller's
	// own writes when the caller supplies an executor, and atomic with the
	// lifecycle event when it does not.
	WithTransaction(ctx context.Context, fn func(q database.SQLQueryExecutor) error) error
}
