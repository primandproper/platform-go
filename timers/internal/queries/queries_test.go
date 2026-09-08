package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/timers/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// These tests read rendered SQL. They cannot say whether Postgres accepts it —
// that is sqlc, which runs over the committed file, and timers' container
// tests, which run it — but they can pin the parts that are silently wrong
// rather than loudly wrong: a lost fence, a lock ordering that stopped ordering,
// a due predicate that drifted from the count that reports on it.

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

// TestTimersTable_IsWhatTheDDLCreates is the cross-check between the canonical
// spelling every statement interpolates and the schema that creates the table.
// Neither derives from the other, which is what makes a rename visible here
// rather than at the first query.
func TestTimersTable_IsWhatTheDDLCreates(t *testing.T) {
	t.Parallel()

	ddl, err := migrations.SQL(dialect.Postgres, "")
	must.NoError(t, err)

	test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+TimersTable+" (")
}

// TestColumns_AreTheTableTheDDLCreates keeps the column list honest. A name that
// the schema does not have is caught by sqlc; a schema column missing from this
// list is not, because the statements would still compile — and this is the list
// the two absences below are measured against.
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

// TestRender_EmitsTheStatementsTheSetExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the timer set does not run.
func TestRender_EmitsTheStatementsTheSetExecutes(T *testing.T) {
	T.Parallel()

	expected := []string{
		"ScheduleTimers",
		"ClaimDueTimers",
		"ReadNextDueTimer",
		"CompleteTimers",
		"ReleaseTimers",
		"CancelTimers",
		"ReapFiredTimers",
		"ReadTimerStats",
	}

	rendered := corpus(T)

	test.MapLen(T, len(expected), rendered)

	for _, name := range expected {
		_, ok := rendered[name]
		test.True(T, ok, test.Sprintf("statement %q is not emitted", name))
	}
}

// TestRender_ScheduleTakesTheNewInstantOutright, in either direction. A merge
// that only ever moved a timer earlier could not express the case the whole
// package exists for: a deadline that moved out.
func TestRender_ScheduleTakesTheNewInstantOutright(T *testing.T) {
	T.Parallel()

	schedule := statement(T, "ScheduleTimers")

	test.StrContains(T, schedule, RunAtColumn+" = excluded."+RunAtColumn)
	test.StrNotContains(T, schedule, "LEAST")
	test.StrNotContains(T, schedule, "GREATEST")

	// A reschedule is a new schedule, not a retry of the old one, so nothing the
	// old one accumulated may survive it.
	test.StrContains(T, schedule, AttemptsColumn+" = 0")
	test.StrContains(T, schedule, LastErrorColumn+" = NULL")
	test.StrContains(T, schedule, FiredAtColumn+" = NULL")
}

// TestRender_ScheduleRevokesTheLeaseOnlyWhenTheInstantMoved. A move frees the
// row, because the lease belongs to a schedule that no longer exists. A
// reschedule to the same instant does not, because that is what an at-least-once
// redelivery looks like and freeing the row there would hand a live firing to a
// second worker.
func TestRender_ScheduleRevokesTheLeaseOnlyWhenTheInstantMoved(T *testing.T) {
	T.Parallel()

	schedule := statement(T, "ScheduleTimers")

	test.StrContains(T, schedule, "WHEN "+querygen.Qualify(TimersTable, RunAtColumn)+
		" IS DISTINCT FROM excluded."+RunAtColumn+" THEN "+epoch)
	test.StrContains(T, schedule, "ELSE "+querygen.Qualify(TimersTable, LeaseColumn))
}

// TestRender_ScheduleLeavesTheDatabaseItsOwnColumns. created_at is the schema's
// DEFAULT: binding it from the writer's clock would put the process that wrote
// the row into a column the server's comparisons are made against. The stamp on
// the other side is the reschedule's, because a reschedule genuinely is the one
// update this table takes that changes what the timer says.
func TestRender_ScheduleLeavesTheDatabaseItsOwnColumns(T *testing.T) {
	T.Parallel()

	schedule := statement(T, "ScheduleTimers")

	inserted, conflict, found := strings.Cut(schedule, "ON CONFLICT")
	must.True(T, found)

	test.StrNotContains(T, inserted, querygen.CreatedAtColumn)
	test.StrNotContains(T, inserted, querygen.LastUpdatedAtColumn)
	test.StrContains(T, conflict, querygen.LastUpdatedAtColumn+" = "+querygen.NowExpression)
	test.StrNotContains(T, conflict, querygen.CreatedAtColumn+" =")
}

// TestRender_ScheduleOrdersItsSourceRows. ON CONFLICT DO UPDATE locks each
// conflicting row as the source reaches it, so two overlapping batches arriving
// in different orders deadlock. One total order over the primary key turns that
// cycle into a queue, and the statement says so rather than trusting its caller
// to have sorted.
func TestRender_ScheduleOrdersItsSourceRows(T *testing.T) {
	T.Parallel()

	test.StrContains(T, statement(T, "ScheduleTimers"), "ORDER BY keys."+KeyColumn)
}

// TestRender_ClaimLimitsAboveTheLock. Postgres applies a LIMIT above the lock,
// so a row a competitor holds is skipped and replaced rather than counted
// against the batch. Pushed into a subquery beneath the lock it would still be
// correct and would quietly halve throughput under contention.
func TestRender_ClaimLimitsAboveTheLock(T *testing.T) {
	T.Parallel()

	claim := statement(T, "ClaimDueTimers")

	limit := strings.Index(claim, "LIMIT sqlc.arg("+ClaimLimitArg+")::int")
	lock := strings.Index(claim, "FOR UPDATE SKIP LOCKED")

	must.Greater(T, -1, limit)
	must.Greater(T, -1, lock)
	test.Greater(T, limit, lock)

	// A second ordering key would let a caller jump a queue of firings that are,
	// by construction, already late.
	test.StrContains(T, claim, "ORDER BY "+querygen.Qualify(TimersTable, RunAtColumn)+
		", "+querygen.Qualify(TimersTable, KeyColumn))
	test.StrNotContains(T, claim, "priority")
}

// TestRender_ClaimHandsBackWhatOnlyItCanKnow: the instant that fences the
// firing, the lateness measured on the server's clock, and whether the lease it
// took over had been somebody else's — a fact that stops existing the moment the
// new lease overwrites it.
func TestRender_ClaimHandsBackWhatOnlyItCanKnow(T *testing.T) {
	T.Parallel()

	claim := statement(T, "ClaimDueTimers")

	test.StrContains(T, claim, "RETURNING\n\t"+querygen.Qualify(TimersTable, KeyColumn))
	test.StrContains(T, claim, querygen.Qualify(TimersTable, RunAtColumn)+",")
	test.StrContains(T, claim, "AS late_microseconds")
	test.StrContains(T, claim, "(due.prior_lease > "+epoch+") AS reclaimed")

	// One attempt counter, incremented server-side by the statement that hands
	// the lease out.
	test.StrContains(T, claim, AttemptsColumn+" = "+
		querygen.Qualify(TimersTable, AttemptsColumn)+" + 1")
}

// TestRender_TheDueReadingIsTheClaimsOwn. A due count that disagreed with what
// the claim will actually hand out is worse than no reading at all, so both are
// rendered from one predicate.
func TestRender_TheDueReadingIsTheClaimsOwn(T *testing.T) {
	T.Parallel()

	rendered := corpus(T)

	test.StrContains(T, rendered["ClaimDueTimers"], duePredicate())
	test.StrContains(T, rendered["ReadTimerStats"], duePredicate())
}

// TestRender_NextDueMeasuresToWhenTheRowBecomesClaimable, which is the later of
// the instant and the lease. Measuring to the instant instead would have a
// poller wake at once and claim nothing for as long as a dead worker's lease had
// left to run.
func TestRender_NextDueMeasuresToWhenTheRowBecomesClaimable(T *testing.T) {
	T.Parallel()

	nextDue := statement(T, "ReadNextDueTimer")

	test.StrContains(T, nextDue, "MIN(GREATEST("+querygen.Qualify(TimersTable, RunAtColumn)+
		", "+querygen.Qualify(TimersTable, LeaseColumn)+"))")

	// The question is "how long until one of these becomes due", so a row
	// excluded for not being due yet would exclude the entire answer.
	test.StrContains(T, nextDue, outstandingPredicate())
	test.StrNotContains(T, nextDue, querygen.Qualify(TimersTable, RunAtColumn)+" <= ")
}

// TestRender_TheKeyedWritesFenceOnTheInstant. Without it, a timer rescheduled
// during its own firing would be retired against the schedule it no longer has.
func TestRender_TheKeyedWritesFenceOnTheInstant(T *testing.T) {
	T.Parallel()

	rendered := corpus(T)

	for _, name := range []string{"CompleteTimers", "ReleaseTimers"} {
		test.StrContains(T, rendered[name],
			"("+querygen.Qualify(TimersTable, KeyColumn)+", "+
				querygen.Qualify(TimersTable, RunAtColumn)+") IN (",
			test.Sprintf("statement %q", name))
	}

	// A cancel is not a firing and carries no instant: "stop the trial-expiry
	// email" is about the timer, whatever it is currently scheduled for.
	test.StrNotContains(T, rendered["CancelTimers"], RunAtsArg)
}

// TestRender_TheKeyedWritesLockInPrimaryKeyOrder. `UPDATE … WHERE timer_key IN
// (…)` gives Postgres no obligation to take row locks in any order, so two
// writers with overlapping key sets can deadlock.
func TestRender_TheKeyedWritesLockInPrimaryKeyOrder(T *testing.T) {
	T.Parallel()

	rendered := corpus(T)

	ordered := "ORDER BY " + querygen.Qualify(TimersTable, SetColumn) +
		", " + querygen.Qualify(TimersTable, KeyColumn) + "\n\tFOR UPDATE\n"

	for _, name := range []string{"CompleteTimers", "ReleaseTimers", "CancelTimers"} {
		test.StrContains(T, rendered[name], ordered, test.Sprintf("statement %q", name))

		// SKIP LOCKED belongs to the reaper alone: these three are recording a
		// decision somebody already acted on, and skipping a locked row would
		// report it as unmatched.
		test.StrNotContains(T, rendered[name], "SKIP LOCKED", test.Sprintf("statement %q", name))
	}
}

// TestRender_CompleteMarksRatherThanDeletes, so "did the expiry run, and when"
// stays answerable until the reaper takes the row.
func TestRender_CompleteMarksRatherThanDeletes(T *testing.T) {
	T.Parallel()

	complete := statement(T, "CompleteTimers")

	test.StrContains(T, complete, FiredAtColumn+" = "+querygen.NowExpression)
	test.StrContains(T, complete, LeaseColumn+" = "+epoch)
	test.StrNotContains(T, complete, "DELETE")

	// A retirement moves this package's own bookkeeping rather than the timer's
	// schedule, so it leaves the row's last mutation alone.
	test.StrNotContains(T, complete, querygen.LastUpdatedAtColumn)
}

// TestRender_ReleasePushesTheInstantOut rather than holding it behind a second
// availability column: this table has one instant per row, and a retried timer
// genuinely is now scheduled for later.
func TestRender_ReleasePushesTheInstantOut(T *testing.T) {
	T.Parallel()

	release := statement(T, "ReleaseTimers")

	test.StrContains(T, release, RunAtColumn+" = "+querygen.NowExpression+" + "+microseconds(DelayArg))

	// Undoing somebody else's retirement would turn the ordinary consequence of
	// a lapsed lease into a loop.
	test.StrContains(T, release, querygen.Qualify(TimersTable, FiredAtColumn)+" IS NULL")

	// The cause binds nullably: a plain hand-back has no error to record, and an
	// empty string would be a failure with nothing to say about itself.
	test.StrContains(T, release, LastErrorColumn+" = sqlc.narg("+LastErrorArg+")")
}

// TestRender_CancelDeletesWhateverTheTimersState. A cancelled timer has no
// history worth keeping, and keeping it would make a reschedule distinguish
// reviving a cancelled timer from reviving a fired one.
func TestRender_CancelDeletesWhateverTheTimersState(T *testing.T) {
	T.Parallel()

	cancel := statement(T, "CancelTimers")

	test.StrContains(T, cancel, "DELETE FROM "+TimersTable)
	test.StrNotContains(T, cancel, FiredAtColumn)
	test.StrContains(T, cancel, querygen.Qualify(TimersTable, KeyColumn)+
		" = ANY(sqlc.arg("+KeysArg+")::text[])")
}

// TestRender_ReapOrdersBeforeItLocks. With one total order, contention between a
// reap and a concurrent write degrades into a queue; without it they deadlock
// the moment they meet.
func TestRender_ReapOrdersBeforeItLocks(T *testing.T) {
	T.Parallel()

	reap := statement(T, "ReapFiredTimers")

	order := strings.Index(reap, "ORDER BY "+querygen.Qualify(TimersTable, SetColumn))
	lock := strings.Index(reap, "FOR UPDATE SKIP LOCKED")

	must.Greater(T, -1, order)
	must.Greater(T, -1, lock)
	test.Greater(T, order, lock)

	// Only fired rows, and only ones past the window. The horizon is subtracted
	// from the server's clock rather than bound from the caller's, because
	// fired_at was stamped by the server — see this package's doc.
	test.StrContains(T, reap, querygen.Qualify(TimersTable, FiredAtColumn)+" IS NOT NULL")
	test.StrContains(T, reap, querygen.Qualify(TimersTable, FiredAtColumn)+" < "+
		querygen.NowExpression+" - "+microseconds(RetentionArg))
	test.StrNotContains(T, reap, "sqlc.arg(horizon)")
}

// TestRender_StatsCountsEveryFieldOverOneSet, in one round trip, because no part
// of the reading is useful alone: a large set is unremarkable if nothing is due
// and an incident if the oldest came due four hours ago.
func TestRender_StatsCountsEveryFieldOverOneSet(T *testing.T) {
	T.Parallel()

	stats := statement(T, "ReadTimerStats")

	for _, column := range []string{
		"AS outstanding", "AS due", "AS leased", "AS stalled", "AS fired",
		"AS oldest_due_microseconds",
	} {
		test.StrContains(T, stats, column, test.Sprintf("projection %q", column))
	}

	// The stall count is only meaningful under a ceiling, so it asks whether
	// there is one rather than reading a zero as "everything has stalled".
	test.StrContains(T, stats, "sqlc.arg("+CeilingArg+")::int > 0")
}

// TestRender_EveryBatchBindsAnArrayRatherThanATuplePerRow. A tuple list makes
// the statement's text a function of the batch size, which is the dynamic SQL
// this tier exists to replace.
func TestRender_EveryBatchBindsAnArrayRatherThanATuplePerRow(T *testing.T) {
	T.Parallel()

	rendered := corpus(T)

	for name, arrays := range map[string][]string{
		"ScheduleTimers": {KeysArg, RunAtsArg, PayloadsArg},
		"CompleteTimers": {KeysArg, RunAtsArg},
		"ReleaseTimers":  {KeysArg, RunAtsArg},
		"CancelTimers":   {KeysArg},
	} {
		for _, argument := range arrays {
			test.StrContains(T, rendered[name], "sqlc.arg("+argument+")::",
				test.Sprintf("statement %q, argument %q", name, argument))
		}
	}

	// The several-column batches are put back together on the position, which is
	// what makes the nth key and the nth instant one row.
	for _, name := range []string{"ScheduleTimers", "CompleteTimers", "ReleaseTimers"} {
		test.StrContains(T, rendered[name], "WITH ORDINALITY", test.Sprintf("statement %q", name))
		test.StrContains(T, rendered[name], "USING ("+ordinal+")", test.Sprintf("statement %q", name))
	}
}

// TestRender_NoStatementNamesAnUnprefixableTable. Every statement carries the
// canonical name, which unison substitutes a consumer's prefix into once at
// construction; a statement that spelled the table some other way would be one
// the substitution misses.
func TestRender_NoStatementNamesAnUnprefixableTable(T *testing.T) {
	T.Parallel()

	for name, body := range corpus(T) {
		test.StrContains(T, body, TimersTable, test.Sprintf("statement %q", name))
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
