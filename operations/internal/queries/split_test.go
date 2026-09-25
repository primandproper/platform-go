package queries

import (
	"os"
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The split corpus, read the way the Postgres one is read above: for the parts
// that are silently wrong rather than loudly wrong. Whether MySQL and SQLite
// accept a statement is sqlc's to say, and whether it behaves is operations'
// container suites'; what can be said from the text is whether a guard, an
// assignment order, a re-check or a rounding direction is still there.

// splitDialects is the roster the split corpus is rendered for.
var splitDialects = []dialect.Dialect{dialect.MySQL, dialect.SQLite}

// splitCorpus is every statement the split corpus renders for one dialect,
// keyed by name.
func splitCorpus(t *testing.T, d dialect.Dialect) map[string]string {
	t.Helper()

	statements := map[string]string{}

	for block := range strings.SplitSeq(Render(d), "-- name: ") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}

		header, body, found := strings.Cut(block, "\n")
		must.True(t, found, must.Sprintf("statement %q has no body", header))

		name, _, _ := strings.Cut(header, " ")
		statements[name] = body
	}

	return statements
}

func splitStatement(t *testing.T, d dialect.Dialect, name string) string {
	t.Helper()

	found, ok := splitCorpus(t, d)[name]
	must.True(t, ok, must.Sprintf("no %s statement named %q", d, name))

	return found
}

// TestRenderSplit_MatchesTheCommittedFiles is the drift gate for the two files
// unison.split.yaml generates from.
func TestRenderSplit_MatchesTheCommittedFiles(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		committed, err := os.ReadFile(FileName(d))
		must.NoError(T, err)

		body := string(committed)
		if index := strings.Index(body, "-- name:"); index > 0 {
			body = body[index:]
		}

		test.EqOp(T, Render(d), body, test.Sprintf("run `make generate` and commit %s", FileName(d)))
	}
}

// TestRenderSplit_EmitsTheStatementsTheStoreExecutes pins the set, for the
// Postgres test's reason.
func TestRenderSplit_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	expected := []string{
		"GetOperationInScope",
		"GetOperation",
		"GetOperations",
		"ListOperations",
		"ListOperationsDescending",
		"InsertOperation",
		"BeginOperation",
		"RecordOperationProgress",
		"GetOperationAck",
		"FinishOperation",
		"FinishOperationWithEveryUnitDone",
		"ReleaseOperation",
		"RequestOperationCancel",
		"ListStrandedOperations",
		"SelectReapableOperations",
		"LockReapableOperations",
		"DeleteReapedOperations",
	}

	for _, d := range splitDialects {
		rendered := splitCorpus(T, d)

		test.MapLen(T, len(expected), rendered, test.Sprintf("dialect %s", d))

		for _, name := range expected {
			_, ok := rendered[name]
			test.True(T, ok, test.Sprintf("%s statement %q is not emitted", d, name))
		}
	}
}

// TestRenderSplit_NothingHandsItsRowBack. Neither engine the split corpus
// serves can be relied on for RETURNING — MySQL has none — which is the whole
// reason it is a second corpus. A RETURNING that crept in here would be a
// statement that works on one of the two.
func TestRenderSplit_NothingHandsItsRowBack(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		test.StrNotContains(T, Render(d), "RETURNING", test.Sprintf("dialect %s", d))
		test.StrNotContains(T, Render(d), "::", test.Sprintf("dialect %s", d))
	}
}

// TestRenderSplit_TheWritesKeepThePostgresGuards. A read-back is only as good
// as the guard in front of it: it runs after a write whose count said it
// matched, so a guard that went missing is a read-back of a row the write had
// no business taking.
func TestRenderSplit_TheWritesKeepThePostgresGuards(T *testing.T) {
	T.Parallel()

	active := "state IN (sqlc.arg(pending_state), sqlc.arg(running_state))"

	for _, d := range splitDialects {
		begin := splitStatement(T, d, "BeginOperation")

		test.StrContains(T, begin, "id = sqlc.arg(id)", test.Sprintf("dialect %s", d))
		test.StrContains(T, begin, active, test.Sprintf("dialect %s", d))
		test.StrContains(T, begin, "claimed_until <= ", test.Sprintf("dialect %s", d))
		test.StrContains(T, begin, "started_at = COALESCE(started_at, ", test.Sprintf("dialect %s", d))

		progress := splitStatement(T, d, "RecordOperationProgress")

		test.StrContains(T, progress, "state = sqlc.arg(running_state)", test.Sprintf("dialect %s", d))
		test.StrContains(T, progress, "units_total = COALESCE(sqlc.narg(units_total), units_total)",
			test.Sprintf("dialect %s", d))

		// The flush's answer repeats the flush's guard, so a read-back can find
		// only the row the flush itself admitted.
		ack := splitStatement(T, d, "GetOperationAck")

		test.StrContains(T, ack, "state = sqlc.arg(running_state)", test.Sprintf("dialect %s", d))

		for _, name := range []string{"FinishOperation", "FinishOperationWithEveryUnitDone", "RequestOperationCancel"} {
			test.StrContains(T, splitStatement(T, d, name), active, test.Sprintf("%s %s", d, name))
		}

		for _, name := range []string{
			"BeginOperation",
			"RecordOperationProgress",
			"FinishOperation",
			"FinishOperationWithEveryUnitDone",
			"ReleaseOperation",
			"RequestOperationCancel",
		} {
			test.StrContains(T, splitStatement(T, d, name), "revision = revision + 1", test.Sprintf("%s %s", d, name))
		}
	}
}

// TestRenderSplit_ProgressIsMonotonicByConstruction, in each engine's spelling
// of the two-argument maximum.
func TestRenderSplit_ProgressIsMonotonicByConstruction(T *testing.T) {
	T.Parallel()

	greatest := map[dialect.Dialect]string{dialect.MySQL: "GREATEST(", dialect.SQLite: "max("}

	for _, d := range splitDialects {
		progress := splitStatement(T, d, "RecordOperationProgress")

		test.StrContains(T, progress, "units_done = "+greatest[d]+"units_done, sqlc.arg(units_done))")
		test.StrContains(T, progress, "progress_count = "+greatest[d]+"progress_count, sqlc.arg(progress_count))")
	}
}

// TestRenderSplit_CancelAssignsTheStateLast. MySQL evaluates a SET list left to
// right and lets a later assignment see an earlier one's result, and every CASE
// in the cancellation tests the state: a state assigned before them would have
// a pending operation's finished_at and lease read a row that is already
// cancelled, and keep neither.
func TestRenderSplit_CancelAssignsTheStateLast(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		cancel := splitStatement(T, d, "RequestOperationCancel")

		assignments, _, found := strings.Cut(cancel, "\nWHERE ")
		must.True(T, found)

		lines := strings.Split(strings.TrimSpace(assignments), "\n")
		test.StrHasPrefix(T,
			"\tstate = CASE WHEN state = sqlc.arg(pending_state) THEN sqlc.arg(cancelled_state) ELSE state END",
			lines[len(lines)-1], test.Sprintf("dialect %s", d))
	}
}

// TestRenderSplit_TheCreateDoesNotIgnoreEverything. MySQL's INSERT IGNORE is
// wider than a duplicate key — it truncates an over-long value and reports
// success — so the create's "do nothing" is a conflict clause on both engines.
func TestRenderSplit_TheCreateDoesNotIgnoreEverything(T *testing.T) {
	T.Parallel()

	want := map[dialect.Dialect]string{
		dialect.MySQL:  "ON DUPLICATE KEY UPDATE id = id",
		dialect.SQLite: "ON CONFLICT (id) DO NOTHING",
	}

	for _, d := range splitDialects {
		insert := splitStatement(T, d, "InsertOperation")

		test.StrNotContains(T, insert, "IGNORE", test.Sprintf("dialect %s", d))
		test.StrContains(T, insert, want[d], test.Sprintf("dialect %s", d))
	}
}

// TestRenderSplit_TheListingBindsTheWholeStateDomain holds the arity to the
// size of operations.State, so a sixth state is a failing test rather than a
// listing that quietly stops returning it. The five are spelled here rather
// than imported, because queries cannot import the package it serves.
func TestRenderSplit_TheListingBindsTheWholeStateDomain(T *testing.T) {
	T.Parallel()

	states := []string{"pending", "running", "succeeded", "failed", "cancelled"}

	test.EqOp(T, len(states), StateFilterArity)
	test.SliceLen(T, StateFilterArity, StateFilterArgs)

	for _, d := range splitDialects {
		for _, name := range []string{"ListOperations", "ListOperationsDescending"} {
			listing := splitStatement(T, d, name)

			// Once in the page, and once in each count beside it.
			for _, arg := range StateFilterArgs {
				test.EqOp(T, 3, strings.Count(listing, "sqlc.arg("+arg+")"), test.Sprintf("%s %s %s", d, name, arg))
			}

			test.StrNotContains(T, listing, "sqlc.slice", test.Sprintf("%s %s", d, name))
		}
	}
}

// TestRenderSplit_SQLiteRoundsEveryWindowTheSafeWay. SQLite's clock is
// millisecond text, so a microsecond count is rounded, and every direction is
// the one that cannot hurt: a lease is rounded up and padded so that it never
// ends before it was asked to, and the grace and retention windows are rounded
// up so that nothing is recovered or reaped early.
func TestRenderSplit_SQLiteRoundsEveryWindowTheSafeWay(T *testing.T) {
	T.Parallel()

	rendered := splitCorpus(T, dialect.SQLite)

	for _, name := range []string{"BeginOperation", "RecordOperationProgress"} {
		test.StrContains(T, rendered[name], "(sqlc.arg(lease_microseconds) + 999) / 1000 + 1",
			test.Sprintf("statement %q", name))
	}

	test.StrContains(T, rendered["ListStrandedOperations"], "((sqlc.arg(grace_microseconds) + 999) / 1000)")
	for _, name := range []string{"SelectReapableOperations", "LockReapableOperations", "DeleteReapedOperations"} {
		test.StrContains(T, rendered[name], "((sqlc.arg(retention_microseconds) + 999) / 1000)",
			test.Sprintf("statement %q", name))
	}

	// And the one clock, at the one grain the schema stores.
	test.StrContains(T, rendered["BeginOperation"], sqliteNow)
}

// TestRenderSplit_TheReapLocksByPrimaryKey. The candidates are read without a
// lock and locked by id, because a MySQL locking read bounded by a range on an
// indexed column next-key-locks the first record past the range — a row this
// reap was never going to delete. SKIP LOCKED on MySQL, where a second reaper
// would otherwise wait on the first; nothing on SQLite, which has no row lock
// to skip. And the ids bind last, where an expanded set cannot be numbered into
// the arguments after it.
func TestRenderSplit_TheReapLocksByPrimaryKey(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		test.StrNotContains(T, splitStatement(T, d, "SelectReapableOperations"), "FOR UPDATE",
			test.Sprintf("dialect %s", d))
	}

	test.StrContains(T, splitStatement(T, dialect.MySQL, "LockReapableOperations"), "FOR UPDATE SKIP LOCKED")
	test.StrNotContains(T, splitStatement(T, dialect.SQLite, "LockReapableOperations"), "FOR UPDATE")

	for _, d := range splitDialects {
		for _, name := range []string{"LockReapableOperations", "DeleteReapedOperations"} {
			statement := splitStatement(T, d, name)

			index := strings.Index(statement, "sqlc.slice(ids)")
			must.Positive(T, index, must.Sprintf("%s %s", d, name))
			test.StrNotContains(T, statement[index:], "sqlc.arg(", test.Sprintf("%s %s", d, name))
		}
	}
}
