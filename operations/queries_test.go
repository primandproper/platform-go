package operations

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// These tests read rendered SQL. They cannot say whether Postgres accepts it —
// that is containers_test.go — but they can pin the parts that are silently
// wrong rather than loudly wrong: a missing guard, a lost lock ordering, a
// GREATEST that became an assignment.

func TestTableFor(T *testing.T) {
	T.Parallel()

	test.EqOp(T, "operations", tableFor(""))
	test.EqOp(T, "ddb_operations", tableFor("ddb"))
}

func TestBuildInsert(T *testing.T) {
	T.Parallel()

	query, args := newTables("").buildInsert(&insertRow{
		id:         "op1",
		kind:       "export",
		owner:      "u1",
		countLabel: "records",
		request:    []byte(`{"a":1}`),
	})

	test.StrContains(T, query, "INSERT INTO operations")
	test.StrContains(T, query, "'pending'")
	must.SliceLen(T, 5, args)
	test.EqOp(T, "op1", args[0])
	test.EqOp(T, "records", args[4])

	// Every timestamp is the server's. A created_at bound from the writer's
	// clock would make the recovery sweep's grace period a comparison between
	// two processes' ideas of a minute.
	test.StrContains(T, query, "now()")
	test.StrNotContains(T, query, "$6")
}

func TestBuildInsert_emptyRequestIsNull(T *testing.T) {
	T.Parallel()

	_, args := newTables("").buildInsert(&insertRow{id: "op1", kind: "export"})

	test.Nil(T, args[3])
}

func TestBuildSelectMany(T *testing.T) {
	T.Parallel()

	query, args := newTables("").buildSelectMany([]string{"a", "b", "c"})

	test.StrContains(T, query, "IN ($1, $2, $3)")
	test.SliceLen(T, 3, args)
	test.StrContains(T, query, operationColumns)
}

func TestBuildList(T *testing.T) {
	T.Parallel()

	T.Run("scoped", func(t *testing.T) {
		t.Parallel()

		query, args := newTables("").buildList(&ListScope{
			Owner:  "u1",
			Kind:   "export",
			States: []State{StatePending, StateRunning},
		}, "", 25, false)

		test.StrContains(t, query, "owner = $1")
		test.StrContains(t, query, "kind = $2")
		test.StrContains(t, query, "state IN ($3, $4)")
		test.StrContains(t, query, "ORDER BY id ASC")
		test.SliceLen(t, 5, args)
	})

	T.Run("a cursor is applied in the sort direction", func(t *testing.T) {
		t.Parallel()

		asc, _ := newTables("").buildList(nil, "cursor", 10, false)
		test.StrContains(t, asc, "id > $1")

		desc, _ := newTables("").buildList(nil, "cursor", 10, true)
		test.StrContains(t, desc, "id < $1")
		test.StrContains(t, desc, "ORDER BY id DESC")
	})

	// A page and the total it is a page of must be the same question, or a
	// client paginates through a set whose size it was told wrongly.
	T.Run("the count shares the listing's predicate", func(t *testing.T) {
		t.Parallel()

		scope := &ListScope{Owner: "u1", States: []State{StateFailed}}

		list, listArgs := newTables("").buildList(scope, "", 10, false)
		count, countArgs := newTables("").buildCount(scope)

		where := func(q string) string {
			_, predicate, _ := strings.Cut(q, "WHERE")

			return predicate
		}

		test.StrContains(t, where(list), "owner = $1")
		test.StrContains(t, where(count), "owner = $1")
		test.EqOp(t, len(countArgs), len(listArgs)-1) // the listing also binds its limit
	})

	T.Run("an empty scope has no predicate", func(t *testing.T) {
		t.Parallel()

		query, _ := newTables("").buildCount(&ListScope{})

		test.StrNotContains(t, query, "WHERE")
	})
}

func TestBuildBegin(T *testing.T) {
	T.Parallel()

	query, args := newTables("").buildBegin("op1", 3, 60_000_000)

	test.StrContains(T, query, "state = 'running'")

	// The three halves of the guard. Any one of them missing is a second worker
	// running an operation somebody else already has.
	test.StrContains(T, query, "id = $1")
	test.StrContains(T, query, activeStatePredicate)
	test.StrContains(T, query, "claimed_until <= now()")

	// A reclaimed operation has been running since the first worker picked it
	// up; moving started_at would erase that.
	test.StrContains(T, query, "started_at = COALESCE(started_at, now())")

	test.StrContains(T, query, "revision = revision + 1")
	test.StrContains(T, query, "RETURNING "+operationColumns)
	test.SliceLen(T, 3, args)
}

func TestBuildProgress(T *testing.T) {
	T.Parallel()

	query, args := newTables("").buildProgress("op1", progressRow{
		unitsTotal: pointer.To(9),
		unitsDone:  3,
		unit:       "identity",
		count:      4300,
		message:    "collecting",
	}, 60_000_000)

	// Monotonic by construction, not by hope. A straggler flush from a worker
	// whose lease lapsed must not walk a client's numbers backwards.
	test.StrContains(T, query, "units_done = GREATEST(units_done, $3)")
	test.StrContains(T, query, "progress_count = GREATEST(progress_count, $5)")

	// A denominator that appeared and then vanished would turn a client's
	// progress bar back into a spinner mid-operation.
	test.StrContains(T, query, "units_total = COALESCE($2, units_total)")

	// The flush is also the lease extension and the cancellation poll. Losing
	// either half here is losing it everywhere, since nothing else does it.
	test.StrContains(T, query, "claimed_until = now() +")
	test.StrContains(T, query, "RETURNING cancel_requested, revision")

	// Guarded on running alone rather than the active set: a flush must not
	// resurrect the progress of an operation somebody else finished, and one
	// arriving before Begin has no lease to extend.
	test.StrContains(T, query, "state = 'running'")

	test.SliceLen(T, 7, args)
}

func TestBuildFinish(T *testing.T) {
	T.Parallel()

	T.Run("a success fills in the last unit", func(t *testing.T) {
		t.Parallel()

		// A Runner that finished every unit but did not report the last one
		// leaves a completed operation reading "8 of 9", which is the single
		// most confusing thing a progress surface can show.
		query, _ := newTables("").buildFinish(finishRow{
			id:           "op1",
			state:        StateSucceeded,
			result:       &Result{URI: "s3://bundle"},
			unitsAllDone: true,
		})

		test.StrContains(t, query, "units_done = COALESCE(units_total, units_done)")
	})

	T.Run("a failure leaves the numerator where it stopped", func(t *testing.T) {
		t.Parallel()

		query, args := newTables("").buildFinish(finishRow{
			id:    "op1",
			state: StateFailed,
			opErr: &Error{Code: "boom", Message: "went wrong", Retryable: true},
		})

		test.StrContains(t, query, "units_done = units_done")
		test.EqOp(t, "boom", args[4])
		test.EqOp(t, true, args[6])
	})

	T.Run("the lease is dropped and the guard is kept", func(t *testing.T) {
		t.Parallel()

		query, _ := newTables("").buildFinish(finishRow{id: "op1", state: StateCancelled})

		test.StrContains(t, query, "claimed_until = "+epoch)
		test.StrContains(t, query, activeStatePredicate)
		test.StrContains(t, query, "finished_at = now()")
	})
}

func TestBuildRequestCancel(T *testing.T) {
	T.Parallel()

	query, args := newTables("").buildRequestCancel("op1")

	// A pending operation is cancelled in the same statement: nothing has
	// started, so there is nothing to ask and nobody to ask it of.
	test.StrContains(T, query, "state = CASE WHEN state = 'pending' THEN 'cancelled' ELSE state END")
	test.StrContains(T, query, "cancel_requested = TRUE")

	// Guarded on the active set, so cancelling a finished operation matches
	// nothing and is not an error — which is what makes a double click safe.
	test.StrContains(T, query, activeStatePredicate)

	test.SliceLen(T, 1, args)
}

func TestBuildSelectStranded(T *testing.T) {
	T.Parallel()

	query, args := newTables("").buildSelectStranded(60_000_000, 200)

	// Two shapes, and they are the same fact seen from either side of Start's
	// two writes.
	test.StrContains(T, query, "state = 'pending' AND updated_at <= now() -")
	test.StrContains(T, query, "state = 'running' AND claimed_until <= now() -")
	test.SliceLen(T, 2, args)
}

func TestBuildReap(T *testing.T) {
	T.Parallel()

	query, args := newTables("").buildReap(60_000_000, 1000)

	// The lock ordering the work queue's documentation opens with: with one
	// total order, contention between a reap and a concurrent write degrades
	// into a queue; without it they deadlock the moment they meet.
	test.StrContains(T, query, "ORDER BY id")
	test.StrContains(T, query, "FOR UPDATE SKIP LOCKED")

	// Only terminal rows, and only ones that actually finished.
	test.StrContains(T, query, "state IN ('succeeded', 'failed', 'cancelled')")
	test.StrContains(T, query, "finished_at IS NOT NULL")

	test.SliceLen(T, 2, args)
}

// Every query builder renders against a prefix, and a prefix that reached only
// some of them would put half the package's statements against a table that
// does not exist.
func TestTables_prefix(T *testing.T) {
	T.Parallel()

	tables := newTables("ddb")

	test.EqOp(T, "ddb", tables.prefix())

	for name, query := range map[string]string{
		"insert":   first(tables.buildInsert(&insertRow{id: "a"})),
		"select":   first(tables.buildSelect("a")),
		"many":     first(tables.buildSelectMany([]string{"a"})),
		"list":     first(tables.buildList(nil, "", 1, false)),
		"count":    first(tables.buildCount(nil)),
		"begin":    first(tables.buildBegin("a", 1, 1)),
		"progress": first(tables.buildProgress("a", progressRow{}, 1)),
		"finish":   first(tables.buildFinish(finishRow{id: "a", state: StateFailed})),
		"release":  first(tables.buildRelease("a", "", "")),
		"cancel":   first(tables.buildRequestCancel("a")),
		"stranded": first(tables.buildSelectStranded(1, 1)),
		"reap":     first(tables.buildReap(1, 1)),
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.StrContains(t, query, "ddb_operations")
		})
	}
}

// first drops a builder's args, for the tests that only read the query text.
func first(query string, _ []any) string { return query }
