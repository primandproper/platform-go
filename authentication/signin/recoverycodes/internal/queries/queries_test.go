package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

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
// nobody runs.
func TestRender_MatchesTheCommittedFiles(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			committed, err := os.ReadFile(FileName(d))
			must.NoError(t, err)

			body := string(committed)
			if index := strings.Index(body, "-- name:"); index > 0 {
				body = body[index:]
			}

			test.EqOp(t, Render(d), body,
				test.Sprintf("run `make generate` and commit %s", FileName(d)))
		})
	}
}

// TestRender_RegistersTheTable is the registry half of the same guarantee: this
// table gets no standard set, so nothing registers it as a side effect of
// emitting one.
func TestRender_RegistersTheTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	test.True(t, querygen.TableRegistered(CodesTable),
		test.Sprintf("%s is not registered", CodesTable))
}

// TestCodesTable_IsTheTableTheDDLCreates cross-checks the canonical spelling here
// against the name the migrations package creates. Neither derives from the
// other, so this is where a rename in one and not the other stops being
// invisible.
func TestCodesTable_IsTheTableTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+CodesTable)
		})
	}
}

// TestColumns_AreTheColumnsTheDDLDeclares keeps the column list and the schema in
// step, naming the column when they drift.
func TestColumns_AreTheColumnsTheDDLDeclares(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			for _, column := range Columns {
				test.StrContains(t, ddl, column, test.Sprintf("column %q", column))
			}
		})
	}
}

// TestColumns_CarryNoConventionTriple pins the absences every predicate here is
// derived from. An id would add an id predicate to every single-row statement,
// and an archived_at would add a liveness predicate that made a replaced set the
// rows a replacement could not reach.
func TestColumns_CarryNoConventionTriple(t *testing.T) {
	t.Parallel()

	for _, column := range []string{
		querygen.IDColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
		querygen.LastIndexedAtColumn,
	} {
		test.False(t, slices.Contains(Columns, column), test.Sprintf("column %q", column))
	}
}

// TestRender_EmitsTheStatementsTheStoreExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	want := []string{
		InsertCodeQuery,
		CodeUnspentQuery,
		SpendCodeQuery,
		CountUnspentCodesQuery,
		ListCodesForUserQuery,
		DeleteCodesForUserQuery,
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			var names []string
			for line := range strings.SplitSeq(rendered, "\n") {
				if after, ok := strings.CutPrefix(line, "-- name: "); ok {
					names = append(names, strings.Fields(after)[0])
				}
			}

			test.SliceEqFunc(t, want, names, func(a, b string) bool { return a == b })

			// Nothing archives one of these rows and the one list is unpaged, so
			// there is no cursor, no filter window and no descending variant.
			test.StrNotContains(t, rendered, querygen.ArchivedAtColumn)
			test.StrNotContains(t, rendered, "page_cursor")
			test.StrNotContains(t, rendered, "result_limit")
			test.StrNotContains(t, rendered, querygen.DescendingSuffix)
		})
	}
}

// TestRender_ProjectsNoHash is the property the list's projection exists for: it
// is the read an export makes, and sixty random bits behind a fast digest is a
// digest somebody could reverse.
func TestRender_ProjectsNoHash(T *testing.T) {
	T.Parallel()

	must.False(T, slices.Contains(RecordColumns, HashColumn))

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			start := strings.Index(rendered, "-- name: "+ListCodesForUserQuery)
			must.True(t, start >= 0)

			list := rendered[start:]
			list, _, _ = strings.Cut(list, ";")

			projection := list[strings.Index(list, "SELECT"):strings.Index(list, "FROM")]
			test.StrNotContains(t, projection, HashColumn)
		})
	}
}

// TestRender_TheSpendRepeatsTheCheck is single use, pinned at the statement: the
// spend's predicate carries every test the check makes, so the check deciding
// nothing is a property of the SQL rather than of a caller's discipline.
func TestRender_TheSpendRepeatsTheCheck(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			start := strings.Index(rendered, "-- name: "+SpendCodeQuery)
			must.True(t, start >= 0)

			spend := rendered[start:]
			spend, _, _ = strings.Cut(spend, ";")

			for _, column := range []string{ScopeColumn, UserIDColumn, HashColumn} {
				test.StrContains(t, spend, column+" = sqlc.arg("+column+")", test.Sprintf("column %q", column))
			}

			test.StrContains(t, spend, UsedAtColumn+" IS NULL")
		})
	}
}
