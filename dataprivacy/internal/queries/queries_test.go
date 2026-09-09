package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/dataprivacy/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// TestRender_MatchesTheCommittedFiles is the regeneration gate, run locally
// rather than only in CI.
//
// The .sql files are what sqlc is run over, and the whole value of running it is
// that they are the statements the store executes. A hand-edit to one — or a
// column list changed without regenerating — would leave sqlc checking SQL
// nobody runs, which is a green check over an unchecked store.
func TestRender_MatchesTheCommittedFiles(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			committed, err := os.ReadFile(FileName(d))
			must.NoError(t, err)

			// The committed file carries the generated-code header, which is
			// the generator's rather than this function's.
			body := string(committed)
			if index := strings.Index(body, "-- name:"); index > 0 {
				body = body[index:]
			}

			test.EqOp(t, Render(d), body,
				test.Sprintf("run `make generate` and commit %s", FileName(d)))
		})
	}
}

// TestRender_RegistersEveryTable is the registry half of the same guarantee the
// canonical .sql files are the query half of: a consumer reading the registry
// back to truncate a database between integration tests finds every table that
// has rows in it, whether or not anything currently emits queries for it.
func TestRender_RegistersEveryTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	for _, table := range TableNames {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

// TestTableNames_AreTheTablesTheDDLCreates is the cross-check between the two
// halves of "what tables does dataprivacy own": the canonical spelling here,
// which the registry and the store's prefix rendering both read, and the list
// migrations.Tables reads out of the DDL for a consumer.
//
// Neither derives from the other on purpose — one is a Go constant a statement
// interpolates, the other is read from the schema that creates the table — so
// this is where a table added to one and not the other stops being invisible.
func TestTableNames_AreTheTablesTheDDLCreates(t *testing.T) {
	t.Parallel()

	created, err := migrations.Tables("")
	must.NoError(t, err)

	declared := slices.Clone(TableNames)
	slices.Sort(declared)

	test.Eq(t, created, declared)
}

// TestRender_EmitsTheStatementsTheStoreExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	// Each paged read appears twice, under its name and that name plus
	// Descending: a sort direction is which way the ORDER BY runs and which way
	// the cursor comparison points, so it is answered by a second statement
	// rather than by a bound argument. And each appears under two names, because
	// the two readings of a scope are two statements rather than a predicate
	// that changes shape.
	want := []string{
		"CreateRequest",
		"GetRequest",
		"ListRequestsForSubject",
		"ListRequestsForSubjectDescending",
		"ListRequestsForSubjectInAnyScope",
		"ListRequestsForSubjectInAnyScopeDescending",
		"ConfirmRequest",
		"CancelRequest",
		"MarkKeyShredded",
		"CompleteExport",
		"CompleteErasure",
		"FailRequest",
		"ExpireArtifact",
		"ListExpiringArtifacts",
		"LapseUnconfirmedRequests",
		"CountOverdueRequests",
		"ReapRequests",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.Eq(t, want, statementNames(Render(d)))
		})
	}
}

// TestColumns_AreTheColumnsTheDDLDeclares is the other half of the schema
// cross-check: a column renamed in the migrations without being renamed here
// would render a corpus sqlc refuses, but a column *added* to the migrations and
// not here renders a corpus that compiles and never reads it.
func TestColumns_AreTheColumnsTheDDLDeclares(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			for _, column := range Columns {
				test.StrContains(t, ddl, "\n    "+column+" ",
					test.Sprintf("%s is not a column the %s DDL declares", column, d))
			}
		})
	}
}

// TestNullable_NamesRealColumns keeps the nullable set from naming a column the
// table does not have, which querygen cannot notice: it renders sqlc.narg for a
// name in this list whether or not the list is a subset of the columns.
func TestNullable_NamesRealColumns(t *testing.T) {
	t.Parallel()

	for _, column := range Nullable {
		test.True(t, slices.Contains(Columns, column), test.Sprintf("nullable %q", column))
	}
}

// TestInsertColumns_SupplyTheCreationTimeAndNothingElseTheDatabaseMaintains
// pins this table's one departure from the module's column convention.
//
// created_at is the instant a statutory response window starts running, and
// due_at is that instant plus the window, computed in Go at submission — so both
// ends of one deadline come from one clock. The other two of the convention
// triple are the database's, as they are everywhere: last_updated_at is stamped
// by every emitted write, and nothing here archives a row.
func TestInsertColumns_SupplyTheCreationTimeAndNothingElseTheDatabaseMaintains(t *testing.T) {
	t.Parallel()

	inserted := InsertColumns()

	test.True(t, slices.Contains(inserted, querygen.CreatedAtColumn))
	test.True(t, slices.Contains(inserted, querygen.IDColumn))
	test.True(t, slices.Contains(inserted, DueAtColumn))

	for _, column := range []string{querygen.LastUpdatedAtColumn, querygen.ArchivedAtColumn} {
		test.False(t, slices.Contains(inserted, column), test.Sprintf("insert assigns %q", column))
	}
}

// TestRender_GuardsSurvive is the assertion that outlives a refactor of any one
// statement: every write that must not race another one still names the value it
// requires the row to still hold.
//
// A guard removed from one of these is a lost race that reports success —
// two confirmations queueing one erasure twice, a completion resurrecting a
// request somebody withdrew — and none of those is visible in a single-threaded
// end-to-end test.
func TestRender_GuardsSurvive(T *testing.T) {
	T.Parallel()

	guarded := []string{
		"ConfirmRequest", "CancelRequest",
		"CompleteExport", "CompleteErasure", "FailRequest", "ExpireArtifact",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statements := statementsOf(Render(d))

			for _, name := range guarded {
				body, ok := statements[name]
				must.True(t, ok, must.Sprintf("%s is not emitted", name))

				test.StrContains(t, body, "status = sqlc.arg("+CurrentStatusArg+")",
					test.Sprintf("%s no longer guards on the status it is replacing", name))

				// The assigned status is a second argument, or the statement
				// would be setting the column to the value it requires it to
				// already hold.
				test.StrContains(t, body, "status = sqlc.arg(status)",
					test.Sprintf("%s no longer assigns a status", name))
			}

			// The shred stamp's guard is the column still being NULL, which is
			// not a value any caller holds: a retried erasure re-shreds, is told
			// the original destruction time, and must not move the timestamp
			// forward to the moment of the retry.
			test.StrContains(t, statements["MarkKeyShredded"], "key_shredded_at IS NULL")
		})
	}
}

// TestRender_TransitionsDifferByTheOperationColumn pins the reason there are two
// transition statements rather than one.
//
// A confirmation records the operation now doing the work, in the same statement
// as the status, so the row cannot become in-progress without saying what is
// fulfilling it. A cancellation must not assign that column at all, because
// blanking it would lose the pointer to an operation that is still running and
// is exactly what a caller watching the cancellation needs.
func TestRender_TransitionsDifferByTheOperationColumn(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statements := statementsOf(Render(d))

			test.StrContains(t, statements["ConfirmRequest"], "operation_id = sqlc.arg(operation_id)")
			test.StrNotContains(t, statements["CancelRequest"], "operation_id")

			// Both leave the confirmation window behind, either by satisfying
			// it or by lapsing it. A stale window would have the lapse sweep
			// pick the row back up.
			for _, name := range []string{"ConfirmRequest", "CancelRequest"} {
				test.StrContains(t, statements[name], "expires_at = sqlc.narg(expires_at)")
			}

			// Only the cancellation is terminal, so only it stamps a completion.
			test.StrContains(t, statements["CancelRequest"], "completed_at = sqlc.narg(completed_at)")
			test.StrNotContains(t, statements["ConfirmRequest"], "completed_at")
		})
	}
}

// TestRender_SweepsReadTerminalityOffCompletedAt pins the fact that replaced the
// two status lists.
//
// Every transition into a terminal state writes completed_at and nothing else
// does, and nothing moves out of one — so "terminal" is completed_at IS NOT NULL
// and "still owed to somebody" is its complement. A statement here that grew a
// status list back would be binding a set inside a bounded write's subquery,
// which is the arrangement querygen documents as silently wrong on two of the
// three dialects.
func TestRender_SweepsReadTerminalityOffCompletedAt(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statements := statementsOf(Render(d))

			test.StrContains(t, statements["ReapRequests"], "completed_at IS NOT NULL")
			test.StrContains(t, statements["CountOverdueRequests"], "completed_at IS NULL")

			for _, name := range []string{"ReapRequests", "CountOverdueRequests"} {
				test.StrNotContains(t, statements[name], "sqlc.slice")
				test.StrNotContains(t, statements[name], "ANY(")
			}
		})
	}
}

// TestRender_ReapNeverTakesARowThatStillNamesAnArtifact is the one property of
// the retention pass that a partially-correct predicate would lose silently.
//
// The reference is the only record of where that object is, and deleting the row
// first would leave a file containing everything known about a person sitting in
// a bucket with nothing left pointing at it.
func TestRender_ReapNeverTakesARowThatStillNamesAnArtifact(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			reap := statementsOf(Render(d))["ReapRequests"]

			test.StrContains(t, reap, "artifact_ref = ''")
			test.StrContains(t, reap, "completed_at <= sqlc.arg("+CompletedBeforeArg+")")

			// The hard delete carries no archived predicate, as no hard delete
			// in this module does: a retention pass that skipped the archived
			// rows would leave behind exactly the records nobody is looking at
			// any more.
			test.StrNotContains(t, reap, "archived_at")
		})
	}
}

// TestRender_BoundedWritesAreMaterializedForMySQLOnly pins the one shape
// difference between the dialects here.
//
// MySQL refuses a subquery reading the table being written (ER_UPDATE_TABLE_USED)
// and accepts the identical rows once they have been materialized through a
// derived table. The other two take the scan directly, and a derived table there
// would be a planner obstacle for no reason.
func TestRender_BoundedWritesAreMaterializedForMySQLOnly(T *testing.T) {
	T.Parallel()

	bounded := []string{"LapseUnconfirmedRequests", "ReapRequests"}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statements := statementsOf(Render(d))

			for _, name := range bounded {
				body := statements[name]

				// The outer key is qualified whatever the dialect: SQLite
				// resolves a bare id against both the statement's target and
				// the subquery's table and calls it ambiguous.
				test.StrContains(t, body, RequestsTable+".id IN (")

				if d == dialect.MySQL {
					test.StrContains(t, body, ") AS bounded")
				} else {
					test.StrNotContains(t, body, ") AS bounded")
				}
			}
		})
	}
}

// TestRender_TheExpirySweepReadsRatherThanWrites is why one of the three sweeps
// is a :many and the other two are writes.
//
// The object has to go before the row says it has. A bulk UPDATE marking rows
// expired would be one round trip and would leave every artifact in the bucket,
// which is precisely the outcome the expired state exists to prevent.
func TestRender_TheExpirySweepReadsRatherThanWrites(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			body := statementsOf(Render(d))["ListExpiringArtifacts"]

			test.StrHasPrefix(t, "SELECT", body)
			test.StrContains(t, body, "artifact_ref <> ''")
			test.StrContains(t, body, "expires_at IS NOT NULL")
			test.StrContains(t, body, "expires_at <= sqlc.arg("+ExpiresBeforeArg+")")
		})
	}
}

// statementNames reads the query names out of a rendered corpus, in the order
// the file lists them.
func statementNames(rendered string) []string {
	var names []string

	for line := range strings.SplitSeq(rendered, "\n") {
		if after, ok := strings.CutPrefix(line, "-- name: "); ok {
			names = append(names, strings.Fields(after)[0])
		}
	}

	return names
}

// statementsOf reads a rendered corpus into its statements, keyed by name.
func statementsOf(rendered string) map[string]string {
	statements := map[string]string{}

	var name string

	var body []string

	flush := func() {
		if name != "" {
			statements[name] = strings.Join(body, "\n")
		}

		body = nil
	}

	for line := range strings.SplitSeq(rendered, "\n") {
		if after, ok := strings.CutPrefix(line, "-- name: "); ok {
			flush()

			name = strings.Fields(after)[0]

			continue
		}

		body = append(body, line)
	}

	flush()

	return statements
}
