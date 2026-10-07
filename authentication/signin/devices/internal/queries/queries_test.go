package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/signin/devices/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// statement returns one named statement out of a rendered file, up to its
// terminator.
func statement(t *testing.T, rendered, name string) string {
	t.Helper()

	start := strings.Index(rendered, "-- name: "+name+" ")
	must.True(t, start >= 0, must.Sprintf("no statement named %q", name))

	body, _, _ := strings.Cut(rendered[start:], ";")

	return body
}

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
				test.Sprintf("run `go run ./internal/queriesgen` and commit %s", FileName(d)))
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

	test.True(t, querygen.TableRegistered(DevicesTable),
		test.Sprintf("%s is not registered", DevicesTable))
}

// TestDevicesTable_IsTheTableTheDDLCreates cross-checks the canonical spelling
// here against the name the migrations package creates. Neither derives from the
// other, so this is where a rename in one and not the other stops being
// invisible.
func TestDevicesTable_IsTheTableTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+DevicesTable+" ")
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
				test.StrContains(t, ddl, "    "+column+" ", test.Sprintf("column %q", column))
			}
		})
	}
}

// TestColumns_CarryNoConventionTriple pins the absences every predicate here is
// derived from. An id would add an id predicate to every single-row statement,
// an archived_at a liveness predicate, and a created_at or last_updated_at
// would be a server-clock stamp beside two columns this store's clock writes.
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
		UpsertDeviceQuery,
		ListDevicesForFamiliesQuery,
		ListDevicesForUserQuery,
		DeleteDevicesForUserQuery,
		SweepDevicesQuery,
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

			// Nothing archives one of these rows and nothing pages them, so
			// there is no cursor, no filter window and no descending variant.
			test.StrNotContains(t, rendered, querygen.ArchivedAtColumn)
			test.StrNotContains(t, rendered, "page_cursor")
			test.StrNotContains(t, rendered, "result_limit")
			test.StrNotContains(t, rendered, querygen.DescendingSuffix)
		})
	}
}

// TestRender_ScopesEveryStatement is the tenancy rule pinned at the SQL: every
// statement filters on the scope but those UnscopedStatements names, and each
// of those says why. The next statement that would read or write across tenants
// fails here and is argued rather than slipped in.
func TestRender_ScopesEveryStatement(T *testing.T) {
	T.Parallel()

	for name, why := range UnscopedStatements {
		test.NotEqOp(T, "", why, test.Sprintf("%s is unscoped without a reason", name))
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			for _, name := range []string{
				UpsertDeviceQuery,
				ListDevicesForFamiliesQuery,
				ListDevicesForUserQuery,
				DeleteDevicesForUserQuery,
				SweepDevicesQuery,
			} {
				body := statement(t, rendered, name)

				// The upsert binds the scope as half the key it converges on;
				// the rest filter on it.
				scoped := strings.Contains(body, "sqlc.arg("+ScopeColumn+")")

				_, exempt := UnscopedStatements[name]
				test.EqOp(t, !exempt, scoped, test.Sprintf("statement %q", name))
			}
		})
	}
}

// TestRender_TheRenewalLeavesTheLoginAlone pins what a refresh may move. The
// owner and the first sighting are what make the row one login rather than a
// token, so the conflict branch assigns neither.
func TestRender_TheRenewalLeavesTheLoginAlone(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			body := statement(t, Render(d), UpsertDeviceQuery)

			_, branch, found := strings.Cut(body, "UPDATE")
			must.True(t, found)

			for _, column := range RenewColumns {
				test.StrContains(t, branch, column+" = ", test.Sprintf("column %q", column))
			}

			for _, column := range []string{ScopeColumn, FamilyIDColumn, UserIDColumn, FirstSeenAtColumn} {
				test.StrNotContains(t, branch, column+" = ", test.Sprintf("column %q", column))
			}
		})
	}
}

// TestRender_TheAnnotatorReadNamesThePerson pins the predicate that keeps a
// caller passing somebody else's family from reading where they signed in from.
func TestRender_TheAnnotatorReadNamesThePerson(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			body := statement(t, Render(d), ListDevicesForFamiliesQuery)

			test.StrContains(t, body, UserIDColumn+" = sqlc.arg("+UserIDColumn+")")
			test.StrContains(t, body, FamilyIDsArg)
		})
	}
}
