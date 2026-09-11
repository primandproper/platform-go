package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/webauthncredentials/migrations"

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
// The .sql files are what sqlc is run over, and the whole value of running it
// is that they are the statements the store executes. A hand-edit to one — or a
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

// TestRender_RegistersTheTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// This table gets no standard set — it has no id and nothing lists it — so
// nothing registers it as a side effect of emitting one. A consumer reading the
// registry back to truncate a database between integration tests would
// otherwise miss it, and the symptom would be a different test failing later on
// rows the previous one left behind.
func TestRender_RegistersTheTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	test.True(t, querygen.TableRegistered(SessionsTable),
		test.Sprintf("%s is not registered", SessionsTable))
}

// TestSessionsTable_IsTheTableTheDDLCreates is the cross-check between the two
// halves of "what table does this store own": the canonical spelling here,
// which the registry and the store's prefix rendering both read, and the name
// the migrations package creates.
//
// Neither derives from the other on purpose — one is a Go constant a statement
// interpolates, the other is in the DDL — so this is where a rename in one and
// not the other stops being invisible.
func TestSessionsTable_IsTheTableTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+SessionsTable)
		})
	}
}

// TestColumns_AreTheColumnsTheDDLDeclares keeps the projection order and the
// schema in step. A column list is what every predicate and every projection
// here is derived from, so a column renamed in the DDL and not here renders SQL
// that sqlc rejects — which is the good failure, and this is the one that names
// the column.
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

// TestColumns_CarryNoConventionColumn pins the absences the rest of this
// package is derived from. querygen renders a statement's predicates from the
// column list it is handed, so an id or an archived_at appearing here would
// silently add a predicate to every statement — and the archived one would make
// the sweep the write unable to reach the rows it exists for.
func TestColumns_CarryNoConventionColumn(t *testing.T) {
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
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	want := []string{
		UpsertSessionQuery,
		GetSessionQuery,
		DeleteSessionQuery,
		SweepExpiredSessionsQuery,
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

			// Nothing lists ceremonies and nothing updates one in place: a
			// challenge is written once, read once, and deleted. The absences
			// are what make the table's shape a decision rather than an
			// oversight.
			test.StrNotContains(t, rendered, "ListSession")
			test.StrNotContains(t, rendered, "\nUPDATE ")
			test.StrNotContains(t, rendered, "archived_at")
		})
	}
}

// TestRender_AddressesEveryRowByItsChallenge is the natural-key half of the
// port. The table has no id, so a statement addressing one row has to say which
// by naming the challenge — and querygen renders no predicate at all for a
// column list with no id, so a statement that named none would be a read of
// every row or a delete of the table.
//
// The two statements not asserted on are the two that address no single row by
// a predicate: the upsert names the challenge as its conflict target instead —
// see TestRender_UpsertConvergesOnTheChallenge — and the sweep is keyed on the
// deadline, which is what makes it a sweep.
func TestRender_AddressesEveryRowByItsChallenge(T *testing.T) {
	T.Parallel()

	keyed := []string{GetSessionQuery, DeleteSessionQuery}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range keyed {
				test.StrContains(t, statement(t, Render(d), name),
					ChallengeColumn+" = sqlc.arg("+ChallengeColumn+")",
					test.Sprintf("%s does not key on the challenge", name))
			}
		})
	}
}

// TestRender_ReadProjectsWhatTheScanReads pins the projection against the
// caller's side of it. The row type the generated querier hands back is built
// from this list in this order, so a column added here without one added there
// is a scan that reads a column into the wrong field.
func TestRender_ReadProjectsWhatTheScanReads(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			read := statement(t, Render(d), GetSessionQuery)

			for _, column := range StateColumns {
				test.StrContains(t, read, querygen.Qualify(SessionsTable, column))
			}

			// The challenge is the key rather than part of the answer: the
			// caller passed it in, and projecting it back would be a column the
			// generated row type carries for nobody.
			test.StrNotContains(t, strings.SplitN(read, "FROM", 2)[0],
				querygen.Qualify(SessionsTable, ChallengeColumn))

			// And the read does not filter on the deadline. Consume compares it
			// in Go, so that an expired row is removed by the delete that
			// follows rather than left behind by a read that could not see it.
			test.StrNotContains(t, strings.SplitN(read, "WHERE", 2)[1], ExpiresAtColumn)
		})
	}
}

// TestRender_SweepComparesAgainstTheServersClock is the one statement whose
// meaning moved in the port, so it is pinned rather than left to the renderer.
//
// The server's clock is what makes the comparison one expression on three
// dialects; a bound instant would be a Go rendering compared against text
// SQLite writes itself. The boundary is inclusive, matching the comparison
// Consume makes, so there is no instant at which a row is neither live nor
// expired.
func TestRender_SweepComparesAgainstTheServersClock(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			sweep := statement(t, Render(d), SweepExpiredSessionsQuery)

			test.StrContains(t, sweep, "DELETE FROM "+SessionsTable)
			test.StrContains(t, sweep, ExpiresAtColumn+" <= "+querygen.NowExpression)
			test.StrNotContains(t, sweep, "sqlc.arg")
			test.StrNotContains(t, sweep, "sqlc.narg")
		})
	}
}

// TestRender_UpsertConvergesOnTheChallenge pins the write's three renderings.
// It is the one statement here whose dialects differ beyond their placeholders,
// and the difference is a grammar rather than a substituted expression.
func TestRender_UpsertConvergesOnTheChallenge(T *testing.T) {
	T.Parallel()

	T.Run("names the conflict target where the dialect has one", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
			upserted := statement(t, Render(d), UpsertSessionQuery)

			test.StrContains(t, upserted, "ON CONFLICT ("+ChallengeColumn+") DO UPDATE SET",
				test.Sprintf("dialect %q", d))
			test.StrContains(t, upserted, SessionDataColumn+" = EXCLUDED."+SessionDataColumn,
				test.Sprintf("dialect %q", d))
			test.StrContains(t, upserted, ExpiresAtColumn+" = EXCLUDED."+ExpiresAtColumn,
				test.Sprintf("dialect %q", d))
		}
	})

	// MySQL names the incoming row through VALUES() and has no conflict target,
	// which is the same statement spelled the only way it accepts.
	T.Run("spells the same write MySQL's way", func(t *testing.T) {
		t.Parallel()

		upserted := statement(t, Render(dialect.MySQL), UpsertSessionQuery)

		test.StrContains(t, upserted, "ON DUPLICATE KEY UPDATE")
		test.StrContains(t, upserted, SessionDataColumn+" = VALUES("+SessionDataColumn+")")
		test.StrNotContains(t, upserted, "ON CONFLICT")
	})

	// The key is never assigned. On Postgres and SQLite that would be a no-op,
	// but on MySQL the collision may have been detected on some other unique
	// key and the assignment would move the row onto the incoming challenge
	// rather than restate it.
	T.Run("never assigns the challenge it converged on", func(T *testing.T) {
		T.Parallel()

		for _, d := range everyDialect {
			T.Run(string(d), func(t *testing.T) {
				t.Parallel()

				_, branch, found := strings.Cut(statement(t, Render(d), UpsertSessionQuery), "UPDATE")
				must.True(t, found)

				test.StrNotContains(t, branch, ChallengeColumn+" =")
			})
		}
	})
}

// namedStatement is one annotated statement out of a rendered corpus.
type namedStatement struct {
	name string
	body string
}

// statements splits a rendered corpus into its annotated statements, so an
// assertion about one is not an assertion about whatever else happens to
// contain the same substring.
func statements(rendered string) []namedStatement {
	var found []namedStatement

	for block := range strings.SplitSeq(rendered, "-- name: ") {
		name, body, ok := strings.Cut(block, "\n")
		if !ok {
			continue
		}

		found = append(found, namedStatement{name: strings.Fields(name)[0], body: body})
	}

	return found
}

// statement returns the body of one named statement, failing when the corpus
// does not carry it.
func statement(t *testing.T, rendered, name string) string {
	t.Helper()

	found := statements(rendered)
	for i := range found {
		if found[i].name == name {
			return found[i].body
		}
	}

	t.Fatalf("no statement named %q", name)

	return ""
}
