package queries

import (
	"os"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v15/series/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// TestRender_MatchesTheCommittedFiles is the regeneration gate, run locally
// rather than only in CI: the .sql files are what sqlc checks and what unison
// generates the executed statements from, so a hand-edit to one leaves sqlc
// checking SQL nobody runs.
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

func TestRender_EmitsEveryStatementTheStoreNames(T *testing.T) {
	T.Parallel()

	names := []string{
		CreateSeriesQuery,
		GetSeriesQuery,
		ListSeriesQuery,
		EndSeriesQuery,
		AdvanceSeriesQuery,
		DueSeriesQuery,
		MaterializeOccurrenceQuery,
		CreateOccurrenceQuery,
		GetOccurrenceQuery,
		ListOccurrencesQuery,
		ListSeriesOccurrencesQuery,
		SkipOccurrenceQuery,
		MoveOccurrenceQuery,
		LinkReplacementQuery,
		SkipWindowQuery,
		SkipSeriesFromQuery,
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			for _, name := range names {
				test.StrContains(t, rendered, "-- name: "+name+" ", test.Sprintf("statement %q", name))
			}

			test.EqOp(t, len(names), strings.Count(rendered, "-- name: "))
		})
	}
}

// TestRender_RegistersTheTables keeps both tables in the registry a consumer
// reads back to truncate a database between integration tests.
func TestRender_RegistersTheTables(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	for _, table := range TableNames {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

func TestColumns_AreTheColumnsTheDDLDeclares(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			for _, table := range TableNames {
				test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+table+" (")
			}

			for _, column := range append(append([]string{}, SeriesColumns...), OccurrenceColumns...) {
				test.StrContains(t, ddl, column, test.Sprintf("column %q", column))
			}
		})
	}
}

// TestEveryConsumerStatementBindsTheScope is the tenancy clause, asserted
// against the rendered corpus. The horizon worker's read is the one exception,
// and the test names it rather than exempting a category.
func TestEveryConsumerStatementBindsTheScope(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, statement := range strings.Split(Render(d), "-- name: ")[1:] {
				name, _, _ := strings.Cut(statement, " ")
				if name == DueSeriesQuery {
					test.StrNotContains(t, statement, "sqlc.arg(scope)")

					continue
				}

				test.StrContains(t, statement, "sqlc.arg(scope)", test.Sprintf("statement %q", name))
			}
		})
	}
}

// TestGuardedWrites_RefuseASkippedOccurrence pins the guard on every write that
// acts on an occurrence: a skipped one is not skipped again, moved, or swept
// into a closure or an end a second time.
func TestGuardedWrites_RefuseASkippedOccurrence(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			for _, name := range []string{SkipOccurrenceQuery, MoveOccurrenceQuery, SkipWindowQuery, SkipSeriesFromQuery} {
				test.StrContains(t, statementNamed(t, rendered, name), "state <> sqlc.arg(skipped_state)",
					test.Sprintf("statement %q", name))
			}
		})
	}
}

// TestMove_LeavesTheSlot pins what keeps the horizon worker from writing a moved
// occurrence's slot a second time: the move never assigns slot_at.
func TestMove_LeavesTheSlot(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.StrNotContains(t, statementNamed(t, Render(d), MoveOccurrenceQuery), "slot_at =")
		})
	}
}

// TestLinkReplacement_RequiresAnUnreplacedSkip pins both halves of the
// replacement's guard.
func TestLinkReplacement_RequiresAnUnreplacedSkip(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), LinkReplacementQuery)

			test.StrContains(t, statement, "state = sqlc.arg(expected_state)")
			test.StrContains(t, statement, "replaced_by = ''")
		})
	}
}

// TestAdvance_NeverMovesBackwards pins the ceiling on the advance.
func TestAdvance_NeverMovesBackwards(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.StrContains(t, statementNamed(t, Render(d), AdvanceSeriesQuery), "materialized_until <= sqlc.arg(through)")
		})
	}
}

// statementNamed returns one rendered statement out of the corpus.
func statementNamed(t *testing.T, rendered, name string) string {
	t.Helper()

	marker := "-- name: " + name + " "

	start := strings.Index(rendered, marker)
	must.True(t, start >= 0, must.Sprintf("statement %q is not in the corpus", name))

	rest := rendered[start:]
	if next := strings.Index(rest[len(marker):], "-- name: "); next >= 0 {
		rest = rest[:len(marker)+next]
	}

	return rest
}
