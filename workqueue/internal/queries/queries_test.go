package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/workqueue/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// These tests read rendered SQL. They cannot say whether Postgres accepts it —
// that is sqlc, which runs over the committed file, and workqueue's container
// tests, which run it — but they can pin the parts that are silently wrong
// rather than loudly wrong: a lock ordering that stopped ordering, a merge rule
// that started revoking leases, a ready count that drifted from the claim that
// reports on it.

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

// TestItemsTable_IsWhatTheDDLCreates is the cross-check between the canonical
// spelling every statement interpolates and the schema that creates the table.
// Neither derives from the other, which is what makes a rename visible here
// rather than at the first query.
func TestItemsTable_IsWhatTheDDLCreates(t *testing.T) {
	t.Parallel()

	ddl, err := migrations.SQL(dialect.Postgres, "")
	must.NoError(t, err)

	test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+ItemsTable+" (")
}

// TestColumns_AreTheTableTheDDLCreates keeps the column list honest. A name the
// schema does not have is caught by sqlc; a schema column missing from this
// list is not, because the statements would still compile.
func TestColumns_AreTheTableTheDDLCreates(t *testing.T) {
	t.Parallel()

	ddl, err := migrations.SQL(dialect.Postgres, "")
	must.NoError(t, err)

	for _, column := range Columns {
		test.StrContains(t, ddl, "\n    "+column+" ", test.Sprintf("column %q", column))
	}

	// Every column the insert names is a column the table has, since the insert
	// is the one statement whose column list is written out rather than derived.
	for _, column := range InsertColumns() {
		test.True(t, slices.Contains(Columns, column), test.Sprintf("column %q", column))
	}
}

// TestColumns_CarryNoConventionTriple. This table is swept rather than
// archived, so archived_at would either do nothing or keep it growing forever,
// and enqueued_at and available_at are the schedule a claim reads rather than a
// creation stamp and a last mutation. The absence is the schema's decision, and
// a statement that started writing one of those columns would be reaching for a
// column the DDL does not create.
func TestColumns_CarryNoConventionTriple(T *testing.T) {
	T.Parallel()

	for _, column := range []string{
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	} {
		test.False(T, slices.Contains(Columns, column), test.Sprintf("column %q", column))
	}

	for name, body := range corpus(T) {
		test.StrNotContains(T, body, querygen.LastUpdatedAtColumn, test.Sprintf("statement %q", name))
		test.StrNotContains(T, body, querygen.ArchivedAtColumn, test.Sprintf("statement %q", name))
	}
}

// TestRender_EmitsTheStatementsTheQueueExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the queue does not run.
func TestRender_EmitsTheStatementsTheQueueExecutes(T *testing.T) {
	T.Parallel()

	expected := []string{
		"EnqueueItems",
		"ClaimDueItems",
		"CompleteItems",
		"ReleaseItems",
		"RemoveItems",
		"ReapCompletedItems",
		"ReadQueueStats",
	}

	rendered := corpus(T)

	test.MapLen(T, len(expected), rendered)

	for _, name := range expected {
		_, ok := rendered[name]
		test.True(T, ok, test.Sprintf("statement %q is not emitted", name))
	}
}

// TestRender_EnqueueMergesOneWay. Priority only rises and availability only
// moves earlier, because an enqueue is a claim on attention and the loudest
// caller should win: without the first, a late quiet enqueue demotes work
// somebody already flagged as urgent.
func TestRender_EnqueueMergesOneWay(T *testing.T) {
	T.Parallel()

	enqueue := statement(T, "EnqueueItems")

	test.StrContains(T, enqueue, PriorityColumn+" = GREATEST("+
		querygen.Qualify(ItemsTable, PriorityColumn)+", excluded."+PriorityColumn+")")
	test.StrContains(T, enqueue, "LEAST("+
		querygen.Qualify(ItemsTable, AvailableAtColumn)+", excluded."+AvailableAtColumn+")")
}

// TestRender_EnqueueRestartsACompletedItemOutright. Restarting is a new unit of
// work wearing an old key, so it takes the new schedule rather than inheriting
// an availability from the past and ignoring the delay the caller asked for.
func TestRender_EnqueueRestartsACompletedItemOutright(T *testing.T) {
	T.Parallel()

	enqueue := statement(T, "EnqueueItems")

	_, conflict, found := strings.Cut(enqueue, "DO UPDATE")
	must.True(T, found)

	for _, assignment := range []string{
		"ELSE excluded." + AvailableAtColumn,
		"ELSE excluded." + EnqueuedAtColumn,
		"ELSE 0",
		"ELSE NULL",
	} {
		test.StrContains(T, conflict, assignment, test.Sprintf("assignment %q", assignment))
	}

	test.StrContains(T, conflict, CompletedAtColumn+" = NULL")
}

// TestRender_EnqueueLeavesAnOutstandingLeaseAlone. Enqueueing an item somebody
// is working on right now must not revoke their lease, and the absence of the
// column from the conflict clause is the whole of that assertion.
func TestRender_EnqueueLeavesAnOutstandingLeaseAlone(T *testing.T) {
	T.Parallel()

	_, conflict, found := strings.Cut(statement(T, "EnqueueItems"), "DO UPDATE")
	must.True(T, found)

	test.StrNotContains(T, conflict, LeaseColumn)
}

// TestRender_EnqueueOrdersItsSourceRows. ON CONFLICT DO UPDATE locks each
// conflicting row as the source reaches it, so two overlapping batches arriving
// in different orders deadlock. One total order over the primary key turns that
// cycle into a queue, and the statement says so rather than trusting its caller
// to have sorted.
func TestRender_EnqueueOrdersItsSourceRows(T *testing.T) {
	T.Parallel()

	test.StrContains(T, statement(T, "EnqueueItems"), "ORDER BY keys."+KeyColumn)
}

// TestRender_ClaimLimitsAboveTheLock. Postgres applies a LIMIT above the lock,
// so a row a competitor holds is skipped and replaced rather than counted
// against the batch. Pushed into a subquery beneath the lock it would still be
// correct and would quietly halve throughput under contention.
func TestRender_ClaimLimitsAboveTheLock(T *testing.T) {
	T.Parallel()

	claim := statement(T, "ClaimDueItems")

	limit := strings.Index(claim, "LIMIT sqlc.arg("+ClaimLimitArg+")::int")
	lock := strings.Index(claim, "FOR UPDATE SKIP LOCKED")

	must.Greater(T, -1, limit)
	must.Greater(T, -1, lock)
	test.Greater(T, limit, lock)

	// The loudest first, then the oldest, with the key breaking the remaining
	// ties so that two claimers walk one backlog in one order.
	test.StrContains(T, claim, "ORDER BY "+querygen.Qualify(ItemsTable, PriorityColumn)+
		" DESC, "+querygen.Qualify(ItemsTable, AvailableAtColumn)+
		", "+querygen.Qualify(ItemsTable, KeyColumn))
}

// TestRender_ClaimHandsBackWhatOnlyItCanKnow: whether the lease it took over
// had been somebody else's, which is a fact that stops existing the moment the
// new lease overwrites it.
func TestRender_ClaimHandsBackWhatOnlyItCanKnow(T *testing.T) {
	T.Parallel()

	claim := statement(T, "ClaimDueItems")

	test.StrContains(T, claim, "RETURNING\n\t"+querygen.Qualify(ItemsTable, KeyColumn))
	test.StrContains(T, claim, "(due.prior_lease > "+epoch+") AS reclaimed")

	// One attempt counter, incremented server-side by the statement that hands
	// the lease out.
	test.StrContains(T, claim, AttemptsColumn+" = "+
		querygen.Qualify(ItemsTable, AttemptsColumn)+" + 1")
}

// TestRender_TheReadyReadingIsTheClaimsOwn. A ready count that disagreed with
// what the claim will actually hand out is worse than no reading at all, so
// both are rendered from one predicate.
func TestRender_TheReadyReadingIsTheClaimsOwn(T *testing.T) {
	T.Parallel()

	rendered := corpus(T)

	test.StrContains(T, rendered["ClaimDueItems"], claimablePredicate())
	test.StrContains(T, rendered["ReadQueueStats"], claimablePredicate())
}

// TestRender_TheKeyedWritesLockInPrimaryKeyOrder. `UPDATE … WHERE item_key IN
// (…)` gives Postgres no obligation to take row locks in any order, so two
// writers with overlapping key sets can deadlock.
func TestRender_TheKeyedWritesLockInPrimaryKeyOrder(T *testing.T) {
	T.Parallel()

	rendered := corpus(T)

	ordered := "ORDER BY " + querygen.Qualify(ItemsTable, QueueColumn) +
		", " + querygen.Qualify(ItemsTable, KeyColumn) + "\n\tFOR UPDATE\n"

	for _, name := range []string{"CompleteItems", "ReleaseItems", "RemoveItems"} {
		test.StrContains(T, rendered[name], ordered, test.Sprintf("statement %q", name))

		// SKIP LOCKED belongs to the reaper alone: these three are recording a
		// decision somebody already acted on, and skipping a locked row would
		// report it as unmatched.
		test.StrNotContains(T, rendered[name], "SKIP LOCKED", test.Sprintf("statement %q", name))
	}
}

// TestRender_CompleteMarksRatherThanDeletes, so a duplicate or a gap can be
// investigated until the reaper takes the row.
func TestRender_CompleteMarksRatherThanDeletes(T *testing.T) {
	T.Parallel()

	complete := statement(T, "CompleteItems")

	test.StrContains(T, complete, CompletedAtColumn+" = "+querygen.NowExpression)
	test.StrContains(T, complete, LeaseColumn+" = "+epoch)
	test.StrNotContains(T, complete, "DELETE")
}

// TestRender_ReleaseHoldsTheItemBackAndRecordsWhy, and skips the ones somebody
// else already finished: undoing their completion would turn the ordinary
// consequence of a lapsed lease into a loop.
func TestRender_ReleaseHoldsTheItemBackAndRecordsWhy(T *testing.T) {
	T.Parallel()

	release := statement(T, "ReleaseItems")

	test.StrContains(T, release, AvailableAtColumn+" = "+
		querygen.NowExpression+" + "+microseconds(DelayArg))
	test.StrContains(T, release, LeaseColumn+" = "+epoch)

	// Inside the CTE rather than after it, so a row the guard excludes is never
	// locked at all.
	before, _, found := strings.Cut(release, "FOR UPDATE")
	must.True(T, found)
	test.StrContains(T, before, outstanding())

	// The cause binds nullably: a plain hand-back has no error to record, and an
	// empty string would be a failure with nothing to say about itself.
	test.StrContains(T, release, LastErrorColumn+" = sqlc.narg("+LastErrorArg+")")
}

// TestRender_RemoveDeletesWhateverTheItemsState. A key whose subject no longer
// exists should stop being scheduled rather than be completed as though the
// work had been done.
func TestRender_RemoveDeletesWhateverTheItemsState(T *testing.T) {
	T.Parallel()

	remove := statement(T, "RemoveItems")

	test.StrContains(T, remove, "DELETE FROM "+ItemsTable)

	before, _, found := strings.Cut(remove, "FOR UPDATE")
	must.True(T, found)
	test.StrNotContains(T, before, CompletedAtColumn)
}

// TestRender_ReapOrdersBeforeItLocks. With one total order, contention between
// a reap and a concurrent write degrades into a queue; without it they deadlock
// the moment they meet.
func TestRender_ReapOrdersBeforeItLocks(T *testing.T) {
	T.Parallel()

	reap := statement(T, "ReapCompletedItems")

	order := strings.Index(reap, "ORDER BY "+querygen.Qualify(ItemsTable, QueueColumn))
	lock := strings.Index(reap, "FOR UPDATE SKIP LOCKED")

	must.Greater(T, -1, order)
	must.Greater(T, -1, lock)
	test.Greater(T, order, lock)

	// Only completed rows, and only ones past the window. The horizon is
	// subtracted from the server's clock rather than bound from the caller's,
	// because completed_at was stamped by the server — see this package's doc.
	test.StrContains(T, reap, querygen.Qualify(ItemsTable, CompletedAtColumn)+" IS NOT NULL")
	test.StrContains(T, reap, querygen.Qualify(ItemsTable, CompletedAtColumn)+" < "+
		querygen.NowExpression+" - "+microseconds(RetentionArg))
	test.StrNotContains(T, reap, "sqlc.arg(horizon)")
}

// TestRender_StatsCountsEveryFieldOverOneQueue, in one round trip, because no
// part of the reading is useful alone: a depth of forty thousand is
// unremarkable if the oldest ready item is four seconds old and an incident if
// it is four hours old.
func TestRender_StatsCountsEveryFieldOverOneQueue(T *testing.T) {
	T.Parallel()

	stats := statement(T, "ReadQueueStats")

	for _, column := range []string{
		"AS pending", "AS ready", "AS leased", "AS stalled", "AS completed",
		"AS oldest_ready_microseconds",
	} {
		test.StrContains(T, stats, column, test.Sprintf("projection %q", column))
	}

	// The stall count is only meaningful under a ceiling, so it asks whether
	// there is one rather than reading a zero as "everything has stalled".
	test.StrContains(T, stats, "sqlc.arg("+CeilingArg+")::int > 0")
}

// TestRender_NoStatementBindsATimestamp. A work queue names no instants at all,
// in either direction: every scheduling comparison is between two server-side
// expressions, and a caller's contribution is a duration. That is what lets
// this package have no clock, and a bound timestamp anywhere here would be the
// end of it.
func TestRender_NoStatementBindsATimestamp(T *testing.T) {
	T.Parallel()

	for name, body := range corpus(T) {
		for _, timeColumn := range []string{
			EnqueuedAtColumn, AvailableAtColumn, LeaseColumn, CompletedAtColumn,
		} {
			test.StrNotContains(T, body, "sqlc.arg("+timeColumn+")",
				test.Sprintf("statement %q binds %q", name, timeColumn))
		}

		// The cast is how an argument would acquire a timestamp type, so its
		// absence catches a bound instant under any name at all.
		test.StrNotContains(T, body, "::timestamptz", test.Sprintf("statement %q", name))
	}
}

// TestRender_EveryBatchBindsAnArrayRatherThanAPlaceholderPerElement. A
// placeholder run makes the statement's text a function of the batch size,
// which is the dynamic SQL this tier exists to replace.
func TestRender_EveryBatchBindsAnArrayRatherThanAPlaceholderPerElement(T *testing.T) {
	T.Parallel()

	rendered := corpus(T)

	for name, arrays := range map[string][]string{
		"EnqueueItems":  {KeysArg, PrioritiesArg, DelayArg},
		"CompleteItems": {KeysArg},
		"ReleaseItems":  {KeysArg},
		"RemoveItems":   {KeysArg},
	} {
		for _, argument := range arrays {
			test.StrContains(T, rendered[name], "sqlc.arg("+argument+")::",
				test.Sprintf("statement %q, argument %q", name, argument))
		}
	}

	// The enqueue is the one batch several columns wide, and its arrays are put
	// back together on the position — which is what makes the nth key, the nth
	// priority and the nth delay one entry.
	test.StrContains(T, rendered["EnqueueItems"], "WITH ORDINALITY")
	test.StrContains(T, rendered["EnqueueItems"], "USING ("+ordinal+")")

	// A one-column batch needs none of that: an item is addressed by its key.
	for _, name := range []string{"CompleteItems", "ReleaseItems", "RemoveItems"} {
		test.StrContains(T, rendered[name], querygen.Qualify(ItemsTable, KeyColumn)+
			" = ANY(sqlc.arg("+KeysArg+")::text[])", test.Sprintf("statement %q", name))
		test.StrNotContains(T, rendered[name], "WITH ORDINALITY", test.Sprintf("statement %q", name))
	}
}

// TestRender_NoStatementNamesAnUnprefixableTable. Every statement carries the
// canonical name, which unison substitutes a consumer's prefix into once at
// construction; a statement that spelled the table some other way would be one
// the substitution misses.
func TestRender_NoStatementNamesAnUnprefixableTable(T *testing.T) {
	T.Parallel()

	for name, body := range corpus(T) {
		test.StrContains(T, body, ItemsTable, test.Sprintf("statement %q", name))
	}
}

// TestRender_RefusesADialectThisPackageHasNoSchemaFor. Every statement is
// written in Postgres, so a MySQL rendering would hand them back unchanged — a
// plausible file for a database that cannot run it, which is the one failure a
// generator can have that nothing downstream would question.
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
