package queries

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/issuereports/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// statementName finds each statement's sqlc annotation.
var statementName = regexp.MustCompile(`(?m)^-- name: (\S+) `)

// TestRender_RegistersEveryTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// This table takes no querygen.Generator.StandardCRUD — which is what registers
// a table elsewhere in this module — so without the explicit registration a
// consumer reading the registry back to truncate a database between integration
// tests would not find it, and the symptom would be a different test failing
// later on rows the previous one left behind.
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
// halves of "what tables does issuereports own": the canonical spelling here,
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

			// The committed file carries the generated-code header, which is the
			// generator's rather than this function's.
			body := string(committed)
			if index := strings.Index(body, "-- name:"); index > 0 {
				body = body[index:]
			}

			test.EqOp(t, Render(d), body,
				test.Sprintf("run `make generate` and commit %s", FileName(d)))
		})
	}
}

// TestRender_EmitsTheStatementsTheStoreExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	// Every paged read appears twice, under its name and that name plus
	// Descending: a sort direction is which way the ORDER BY runs and which way
	// the cursor comparison points, so it is answered by a second statement
	// rather than by a bound argument.
	want := []string{
		"CreateReport", "UpdateReport", "TransitionReport", "ArchiveReport", "DeleteReportsByReporter",
		"GetReport", "GetReportCreatedAt",
		"ListReports", "ListReportsDescending",
		"ListReportsByStatus", "ListReportsByStatusDescending",
		"ListReportsByReporter", "ListReportsByReporterDescending",
		"ListReportsBySubjectType", "ListReportsBySubjectTypeDescending",
		"ListReportsForSubject", "ListReportsForSubjectDescending",
	}

	slices.Sort(want)

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			got := statementNames(Render(d))
			slices.Sort(got)

			test.Eq(t, want, got)
		})
	}
}

// TestRender_ScopesEveryStatement is the tenancy doctrine asserted over the text
// rather than over the store.
//
// There is no exception in this corpus, and that is the assertion: every
// statement names the scope, and the one that keys on nothing — the insert —
// names it as a column it stores. A statement added without one would be a read
// that answers across tenants, and nothing about its result would say so.
func TestRender_ScopesEveryStatement(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, body := range statements(Render(d)) {
				test.StrContains(t, body, ScopeColumn,
					test.Sprintf("%s does not name the scope", name))

				if strings.Contains(body, "WHERE") {
					test.StrContains(t, body, ScopeColumn+" =",
						test.Sprintf("%s does not filter on the scope", name))
				}
			}
		})
	}
}

// TestRender_GuardsTheTransition pins the predicate the lifecycle rests on.
//
// The transition assigns the status and requires the row to still hold the one
// its caller read, and the two ends are two argument names. Under one name the
// statement would set the column to the value it was requiring it to already
// hold — legal SQL that guards nothing — and two triagers resolving the same
// report would both be told they won.
func TestRender_GuardsTheTransition(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			body := statements(Render(d))["TransitionReport"]
			must.NotEq(t, "", body)

			test.StrContains(t, body, StatusColumn+" = ",
				test.Sprint("the transition must assign the status"))
			test.StrContains(t, body, CurrentStatusArg,
				test.Sprint("the transition must guard on the status the caller read"))
			test.StrContains(t, body, ClosedAtColumn,
				test.Sprint("the transition must move the stamp with the status"))
			test.StrContains(t, body, ResolutionColumn,
				test.Sprint("the transition must move the note with the status"))
		})
	}
}

// TestRender_UpdateCannotMoveTheStatus is the other half of that guarantee.
//
// The lifecycle has one door. A revision that also assigned the status would be
// a door around the guard: a client PUTting a whole report would reopen one
// somebody had just resolved, with nothing anywhere reporting it.
func TestRender_UpdateCannotMoveTheStatus(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			body := statements(Render(d))["UpdateReport"]
			must.NotEq(t, "", body)

			set, _, found := strings.Cut(body, "WHERE")
			must.True(t, found, must.Sprint("the revision must key on something"))

			for _, column := range []string{StatusColumn, ClosedAtColumn, ResolutionColumn, ReporterColumn} {
				test.StrNotContains(t, set, column,
					test.Sprintf("the revision assigns %s, which is not the reporter's to change", column))
			}
		})
	}
}

// TestRender_ErasureReachesArchivedRows is the one statement whose absence of a
// predicate is the point.
//
// A subject access request runs against whatever the subject left behind, and a
// report they filed and somebody archived is still a sentence they wrote. A
// delete that excluded archived rows would leave exactly those behind, and the
// erasure would report success.
func TestRender_ErasureReachesArchivedRows(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			body := statements(Render(d))["DeleteReportsByReporter"]
			must.NotEq(t, "", body)

			test.StrContains(t, body, "DELETE FROM")
			test.StrContains(t, body, ReporterColumn+" =")
			test.StrNotContains(t, body, querygen.ArchivedAtColumn,
				test.Sprint("an erasure that skipped archived rows would leave the subject's words behind"))
		})
	}
}

func TestTable_ColumnsExcept(t *testing.T) {
	t.Parallel()

	// Leaving the id out is how a statement says it keys on something else, so
	// the order of what remains has to be the projection's — the generated
	// params struct follows it.
	test.Eq(t, Reports.Columns[1:], Reports.ColumnsExcept(querygen.IDColumn))
	test.SliceNotContains(t, Reports.ColumnsExcept(querygen.IDColumn), querygen.IDColumn)

	test.Eq(t, Reports.Columns, Reports.ColumnsExcept())
}

func TestTable_InsertColumns(t *testing.T) {
	t.Parallel()

	// created_at is the database's, which is why the schema gives it a DEFAULT.
	test.SliceNotContains(t, Reports.InsertColumns(), querygen.CreatedAtColumn)
	test.SliceNotContains(t, Reports.InsertColumns(), querygen.LastUpdatedAtColumn)
	test.SliceNotContains(t, Reports.InsertColumns(), querygen.ArchivedAtColumn)
	test.SliceContains(t, Reports.InsertColumns(), StatusColumn)
	test.SliceContains(t, Reports.InsertColumns(), ClosedAtColumn)
}

func TestFileName(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "postgres_generated.sql", FileName(dialect.Postgres))
}

// statements splits a rendered corpus into its named statements.
func statements(rendered string) map[string]string {
	out := map[string]string{}

	matches := statementName.FindAllStringSubmatchIndex(rendered, -1)
	for i, m := range matches {
		end := len(rendered)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}

		out[rendered[m[2]:m[3]]] = rendered[m[0]:end]
	}

	return out
}

// statementNames is the names alone, for the set assertion.
func statementNames(rendered string) []string {
	names := make([]string, 0, len(statements(rendered)))
	for name := range statements(rendered) {
		names = append(names, name)
	}

	return names
}
