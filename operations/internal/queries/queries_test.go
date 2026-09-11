package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/operations/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// These tests read rendered SQL. They cannot say whether Postgres accepts it —
// that is sqlc, which runs over the committed file, and operations'
// container tests, which run it — but they can pin the parts that are silently
// wrong rather than loudly wrong: a missing guard, a lost lock ordering, a
// GREATEST that became an assignment.

// corpus is every statement this package renders, keyed by name.
func corpus(t *testing.T) map[string]string {
	t.Helper()

	statements := map[string]string{}

	for block := range strings.SplitSeq(Render(dialect.Postgres), "-- name: ") {
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

func statement(t *testing.T, name string) string {
	t.Helper()

	found, ok := corpus(t)[name]
	must.True(t, ok, must.Sprintf("no statement named %q", name))

	return found
}

// TestRender_MatchesTheCommittedFile is the drift gate. The committed .sql is
// what sqlc reads and what unison generates from, so a rendering that has moved
// on from it is a corpus checked against statements nobody runs.
func TestRender_MatchesTheCommittedFile(T *testing.T) {
	T.Parallel()

	committed, err := os.ReadFile(FileName(dialect.Postgres))
	must.NoError(T, err)

	// The committed file carries the generated-code header, which is the
	// generator's rather than this function's.
	body := string(committed)
	if index := strings.Index(body, "-- name:"); index > 0 {
		body = body[index:]
	}

	test.EqOp(T, Render(dialect.Postgres), body,
		test.Sprintf("run `make generate` and commit %s", FileName(dialect.Postgres)))
}

// TestRender_RegistersTheTable is the registry half of the same guarantee the
// canonical .sql is the query half of: a consumer reading the registry back to
// truncate a database between integration tests must find this table in it.
func TestRender_RegistersTheTable(t *testing.T) {
	t.Parallel()

	_ = Render(dialect.Postgres)

	for _, table := range TableNames {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

// TestOperationsTable_IsWhatTheDDLCreates is the cross-check between the
// canonical spelling every statement interpolates and the schema that creates
// the table. Neither derives from the other, which is what makes a rename
// visible here rather than at the first query.
func TestOperationsTable_IsWhatTheDDLCreates(t *testing.T) {
	t.Parallel()

	ddl, err := migrations.SQL(dialect.Postgres, "")
	must.NoError(t, err)

	test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+OperationsTable+" (")
}

// TestColumns_AreTheTableTheDDLCreates keeps the projection honest. A column
// list that names something the schema does not have is caught by sqlc; a
// schema column missing from the list is not, because the statements would
// still compile — and a column nothing projects is a column no client ever
// sees.
func TestColumns_AreTheTableTheDDLCreates(t *testing.T) {
	t.Parallel()

	ddl, err := migrations.SQL(dialect.Postgres, "")
	must.NoError(t, err)

	for _, column := range Columns {
		test.StrContains(t, ddl, "\n    "+column+" ", test.Sprintf("column %q", column))
	}

	// The two the projection deliberately leaves out, so that dropping one from
	// the schema is a decision rather than a silent agreement with this list.
	for _, absent := range []string{querygen.ArchivedAtColumn, "claimed_until"} {
		test.False(t, contains(Columns, absent), test.Sprintf("column %q", absent))
		test.StrContains(t, ddl, "\n    "+absent+" ", test.Sprintf("column %q", absent))
	}
}

// TestRender_EmitsTheStatementsTheStoreExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	expected := []string{
		"GetOperationInScope",
		"GetOperation",
		"GetOperations",
		"ListOperations",
		"ListOperationsDescending",
		"CreateOperation",
		"BeginOperation",
		"RecordOperationProgress",
		"FinishOperation",
		"FinishOperationWithEveryUnitDone",
		"ReleaseOperation",
		"RequestOperationCancel",
		"ListStrandedOperations",
		"ReapOperations",
	}

	rendered := corpus(T)

	test.MapLen(T, len(expected), rendered)

	for _, name := range expected {
		_, ok := rendered[name]
		test.True(T, ok, test.Sprintf("statement %q is not emitted", name))
	}
}

// TestRender_CreateLeavesTheDatabaseItsOwnColumns keeps the insert off the
// columns the schema fills in.
//
// created_at, revision and claimed_until are the schema's DEFAULTs. Binding
// created_at from the writer's clock would make the recovery sweep's grace
// period a comparison between two processes' ideas of a minute.
func TestRender_CreateLeavesTheDatabaseItsOwnColumns(T *testing.T) {
	T.Parallel()

	create := statement(T, "CreateOperation")

	inserted, _, found := strings.Cut(create, " VALUES ")
	must.True(T, found)

	for _, column := range []string{querygen.CreatedAtColumn, "revision", "claimed_until", querygen.LastUpdatedAtColumn} {
		test.StrNotContains(T, inserted, column, test.Sprintf("column %q", column))
	}

	// A collision returns no rows and leaves the surrounding transaction
	// healthy, which is what makes the idempotency seam usable from inside one.
	test.StrContains(T, create, "ON CONFLICT (id) DO NOTHING")
	test.StrContains(T, create, "RETURNING\n\t"+strings.Join(Columns, ",\n\t"))
}

// TestRender_BeginKeepsAllThreeHalvesOfItsGuard. Any one of them missing is a
// second worker running an operation somebody else already has.
func TestRender_BeginKeepsAllThreeHalvesOfItsGuard(T *testing.T) {
	T.Parallel()

	begin := statement(T, "BeginOperation")

	test.StrContains(T, begin, "id = sqlc.arg(id)")
	test.StrContains(T, begin, "state = ANY(sqlc.arg(active_states)::text[])")
	test.StrContains(T, begin, "claimed_until <= "+querygen.NowExpression)

	// A reclaimed operation has been running since the first worker picked it
	// up; moving started_at would erase that.
	test.StrContains(T, begin, "started_at = COALESCE(started_at, "+querygen.NowExpression+")")

	test.StrContains(T, begin, "revision = revision + 1")
	test.StrContains(T, begin, "RETURNING\n\t"+strings.Join(Columns, ",\n\t"))
}

// TestRender_ProgressIsMonotonicByConstruction. A straggler flush from a worker
// whose lease lapsed must not walk a client's numbers backwards.
func TestRender_ProgressIsMonotonicByConstruction(T *testing.T) {
	T.Parallel()

	progress := statement(T, "RecordOperationProgress")

	test.StrContains(T, progress, "units_done = GREATEST(units_done, sqlc.arg(units_done))")
	test.StrContains(T, progress, "progress_count = GREATEST(progress_count, sqlc.arg(progress_count))")

	// A denominator that appeared and then vanished would turn a client's
	// progress bar back into a spinner mid-operation.
	test.StrContains(T, progress, "units_total = COALESCE(sqlc.narg(units_total), units_total)")

	// The flush is also the lease extension and the cancellation poll. Losing
	// either half here is losing it everywhere, since nothing else does it.
	test.StrContains(T, progress, "claimed_until = "+querygen.NowExpression+" +")
	test.StrContains(T, progress, "RETURNING cancel_requested, revision")

	// Guarded on running alone rather than the active set: a flush must not
	// resurrect the progress of an operation somebody else finished, and one
	// arriving before the claim has no lease to extend.
	test.StrContains(T, progress, "state = sqlc.arg(running_state)")
}

// TestRender_FinishIsTwoStatements. The conditional SET this replaced was the
// last piece of dynamic SQL in the package, and the two forms differ in exactly
// one assignment.
func TestRender_FinishIsTwoStatements(T *testing.T) {
	T.Parallel()

	rendered := corpus(T)

	plain := rendered["FinishOperation"]
	everyUnit := rendered["FinishOperationWithEveryUnitDone"]

	// A Runner that finished every unit but did not report the last one leaves a
	// completed operation reading "8 of 9", which is the single most confusing
	// thing a progress surface can show.
	test.StrContains(T, everyUnit, "units_done = COALESCE(units_total, units_done)")

	// A run that stopped short keeps the counter where it stopped, which is why
	// this is two statements rather than one that decides.
	test.StrNotContains(T, plain, "units_done")

	// The lease is dropped outright: nothing will claim the operation again, and
	// a claimed_until left in the future is a row every recovery sweep still has
	// to consider.
	for name, finish := range map[string]string{"FinishOperation": plain, "FinishOperationWithEveryUnitDone": everyUnit} {
		test.StrContains(T, finish, "claimed_until = "+epoch, test.Sprintf("statement %q", name))
		test.StrContains(T, finish, "state = ANY(sqlc.arg(active_states)::text[])", test.Sprintf("statement %q", name))
		test.StrContains(T, finish, "finished_at = "+querygen.NowExpression, test.Sprintf("statement %q", name))
	}

	test.EqOp(T, strings.ReplaceAll(everyUnit, "\n\tunits_done = COALESCE(units_total, units_done),", ""), plain)
}

// TestRender_CancelResolvesAPendingOperationInTheSameStatement. Split into a
// read and a write, the decision would be taken against a state the row may
// have left by the time the write lands.
func TestRender_CancelResolvesAPendingOperationInTheSameStatement(T *testing.T) {
	T.Parallel()

	cancel := statement(T, "RequestOperationCancel")

	test.StrContains(T, cancel, "cancel_requested = TRUE")
	test.StrContains(T, cancel,
		"state = CASE WHEN state = sqlc.arg(pending_state) THEN sqlc.arg(cancelled_state) ELSE state END")

	// Guarded on the active set, so cancelling a finished operation matches
	// nothing and is not an error — which is what makes a double click safe.
	test.StrContains(T, cancel, "state = ANY(sqlc.arg(active_states)::text[])")
}

// TestRender_EveryWriteStampsTheLastMutation, so a watcher can tell a re-read
// that changed from one that did not without comparing every column.
func TestRender_EveryWriteStampsTheLastMutation(T *testing.T) {
	T.Parallel()

	rendered := corpus(T)

	for _, name := range []string{
		"BeginOperation",
		"RecordOperationProgress",
		"FinishOperation",
		"FinishOperationWithEveryUnitDone",
		"ReleaseOperation",
		"RequestOperationCancel",
	} {
		test.StrContains(T, rendered[name], "last_updated_at = "+querygen.NowExpression,
			test.Sprintf("write %q", name))
		test.StrContains(T, rendered[name], "revision = revision + 1", test.Sprintf("write %q", name))
	}
}

// TestRender_StrandedIsTheSameFactFromEitherSide of a start's two writes: an
// operation recorded but never offered, and one offered to a worker that died.
func TestRender_StrandedIsTheSameFactFromEitherSide(T *testing.T) {
	T.Parallel()

	stranded := statement(T, "ListStrandedOperations")

	test.StrContains(T, stranded,
		"operations.state = sqlc.arg(pending_state) AND operations.created_at <= "+querygen.NowExpression+" -")
	test.StrContains(T, stranded,
		"operations.state = sqlc.arg(running_state) AND operations.claimed_until <= "+querygen.NowExpression+" -")

	// The pending arm reads created_at rather than last_updated_at: a
	// cancellation request touches a pending row without starting it, and must
	// not restart the clock on how long it has waited.
	test.StrContains(T, stranded, "ORDER BY operations.created_at ASC")
}

// TestRender_ReapOrdersBeforeItLocks. With one total order, contention between
// a reap and a concurrent write degrades into a queue; without it they deadlock
// the moment they meet.
func TestRender_ReapOrdersBeforeItLocks(T *testing.T) {
	T.Parallel()

	reap := statement(T, "ReapOperations")

	order := strings.Index(reap, "ORDER BY operations.id ASC")
	lock := strings.Index(reap, "FOR UPDATE SKIP LOCKED")

	must.Greater(T, -1, order)
	must.Greater(T, -1, lock)
	test.Greater(T, order, lock)

	// Only terminal rows, and only ones that actually finished.
	test.StrContains(T, reap, "state = ANY(sqlc.arg(terminal_states)::text[])")
	test.StrContains(T, reap, "finished_at IS NOT NULL")
}

// TestRender_TheListingNarrowsInThreeShapes. Kind is an open set, so an absent
// one narrows nothing; state is closed, so it is a bound set and "every state"
// is a value the caller sends; the scope is a plain equality with no absent
// form at all, because the absent form of a tenancy predicate is every tenant.
func TestRender_TheListingNarrowsInThreeShapes(T *testing.T) {
	T.Parallel()

	rendered := corpus(T)

	for _, name := range []string{"ListOperations", "ListOperationsDescending"} {
		listing := rendered[name]

		test.StrContains(T, listing, "sqlc.narg(kind)::text IS NULL OR operations.kind = sqlc.narg(kind)",
			test.Sprintf("statement %q", name))

		// No optional form of the scope anywhere in the statement. A narg on
		// this column would be a listing whose caller can ask for every tenant's
		// operations by leaving an argument nil.
		test.StrNotContains(T, listing, "sqlc.narg(scope)", test.Sprintf("statement %q", name))

		// The page and both counts, on the scope as on the state — a count taken
		// without the scope would tell a client how many operations everybody
		// has while showing them their own.
		for _, predicate := range []string{
			"operations.scope = sqlc.arg(scope)",
			"operations.state = ANY(sqlc.arg(states)::text[])",
		} {
			test.EqOp(T, 3, strings.Count(listing, predicate),
				test.Sprintf("statement %q, predicate %q", name, predicate))
		}
	}

	test.StrContains(T, rendered["ListOperations"], "ORDER BY operations.id ASC")
	test.StrContains(T, rendered["ListOperationsDescending"], "ORDER BY operations.id DESC")
}

// TestRender_TheScopedReadsBindTheScope pins the pair the consumer reads
// through, and the one statement that deliberately does not.
//
// The unscoped GetOperation is not an oversight and not a variant a read may
// reach for: it is the read-back at the end of RequestOperationCancel, which is
// machinery holding an id it has just written. What would be a defect is a
// second unscoped read appearing beside it, so the count is pinned rather than
// the presence.
func TestRender_TheScopedReadsBindTheScope(T *testing.T) {
	T.Parallel()

	rendered := corpus(T)

	test.StrContains(T, rendered["GetOperationInScope"], "operations.scope = sqlc.arg(scope)")
	test.StrContains(T, rendered["GetOperations"], "operations.scope = sqlc.arg(scope)")

	// The bound set trails the scope, which is what keeps the set the last
	// argument in the statement.
	test.StrContains(T, rendered["GetOperations"],
		"WHERE operations.scope = sqlc.arg(scope)\n\tAND operations.id = ANY(sqlc.arg(ids)::text[])")

	test.StrNotContains(T, rendered["GetOperation"], "scope = sqlc.arg(scope)")

	unscoped := 0

	for name, body := range rendered {
		if strings.HasPrefix(name, "Get") && !strings.Contains(body, "sqlc.arg(scope)") {
			unscoped++
		}
	}

	test.EqOp(T, 1, unscoped)
}

// TestRender_NoStatementNamesAnUnprefixableTable. Every statement carries the
// canonical name, which unison substitutes a consumer's prefix into once at
// construction; a statement that spelled the table some other way would be one
// the substitution misses.
func TestRender_NoStatementNamesAnUnprefixableTable(T *testing.T) {
	T.Parallel()

	for name, body := range corpus(T) {
		test.StrContains(T, body, OperationsTable, test.Sprintf("statement %q", name))
	}
}

// TestInsertColumns_AreColumnsTheTableHas, since the insert is the one
// statement whose column list is written out rather than derived from Columns.
func TestInsertColumns_AreColumnsTheTableHas(t *testing.T) {
	t.Parallel()

	for _, column := range InsertColumns() {
		test.True(t, contains(Columns, column), test.Sprintf("column %q", column))
	}
}

func contains(columns []string, column string) bool {
	return slices.Contains(columns, column)
}

// TestRender_RefusesADialectThisPackageHasNoSchemaFor. The transitions are
// written in Postgres, so a MySQL rendering would hand them back unchanged —
// a plausible file for a database that cannot run it, which is the one failure
// a generator can have that nothing downstream would question.
func TestRender_RefusesADialectThisPackageHasNoSchemaFor(T *testing.T) {
	T.Parallel()

	for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite} {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			defer func() {
				value := recover()
				must.NotNil(t, value, must.Sprintf("dialect %q rendered", d))

				err, ok := value.(error)
				must.True(t, ok, must.Sprintf("panicked with %T rather than an error", value))
				test.ErrorIs(t, err, dialect.ErrUnsupported)
			}()

			_ = Render(d)
		})
	}
}

// TestFileName names the committed file the pipeline reads.
func TestFileName(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "postgres_generated.sql", FileName(dialect.Postgres))
}
