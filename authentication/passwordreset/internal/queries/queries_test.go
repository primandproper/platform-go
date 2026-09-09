package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations"

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
// This table gets no standard set — nothing lists these rows — so nothing
// registers it as a side effect of emitting one. A consumer reading the registry
// back to truncate a database between integration tests would otherwise miss it,
// and the symptom would be a different test failing later on rows the previous
// one left behind.
func TestRender_RegistersTheTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	test.True(t, querygen.TableRegistered(TokensTable),
		test.Sprintf("%s is not registered", TokensTable))
}

// TestTokensTable_IsTheTableTheDDLCreates is the cross-check between the two
// halves of "what table does this store own": the canonical spelling here,
// which the registry and the store's prefix rendering both read, and the name
// the migrations package creates.
//
// Neither derives from the other on purpose — one is a Go constant a statement
// interpolates, the other is in the DDL — so this is where a rename in one and
// not the other stops being invisible.
func TestTokensTable_IsTheTableTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+TokensTable)
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

// TestColumns_CarryNoSoftDelete pins the absences the rest of this package is
// derived from. querygen renders a statement's predicates from the column list
// it is handed, so an archived_at appearing here would silently add a predicate
// to every read — and would make the sweep and the revocation the two writes
// unable to reach the rows they exist for.
//
// last_updated_at is the other one, and it is why the redemption assigns one
// column rather than two: querygen stamps that column wherever a column list
// carries it, and a stamp beside redeemed_at would be a second record of the
// one mutation this row has.
func TestColumns_CarryNoSoftDelete(t *testing.T) {
	t.Parallel()

	for _, column := range []string{
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
		querygen.LastIndexedAtColumn,
	} {
		test.False(t, slices.Contains(Columns, column), test.Sprintf("column %q", column))
	}
}

// TestKeyedColumns_AreTheTableWithoutItsID pins the idiom every statement keyed
// on something other than the id depends on. querygen renders the id predicate
// from the column list, so a list that grew one back would silently narrow the
// lookup, the revocation and the sweep to a single row nobody named.
func TestKeyedColumns_AreTheTableWithoutItsID(t *testing.T) {
	t.Parallel()

	test.False(t, slices.Contains(KeyedColumns, querygen.IDColumn))
	test.EqOp(t, len(Columns)-1, len(KeyedColumns))

	for _, column := range Columns {
		if column == querygen.IDColumn {
			continue
		}

		test.True(t, slices.Contains(KeyedColumns, column), test.Sprintf("column %q", column))
	}
}

// TestRender_EmitsTheStatementsTheStoreExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	want := []string{
		InsertTokenQuery,
		GetTokenByDigestQuery,
		RedeemTokenQuery,
		RevokeTokensForUserQuery,
		SweepExpiredTokensQuery,
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

			// Nothing lists these rows and nothing archives one: a token is
			// written once, read by its digest, stamped once, and deleted. The
			// absences are what make the table's shape a decision rather than
			// an oversight.
			test.StrNotContains(t, rendered, "ListToken")
			test.StrNotContains(t, rendered, querygen.ArchivedAtColumn)
		})
	}
}

// TestRender_ProjectsNoDigest is the property the whole projection exists for,
// and the one a SELECT * with a Go-side drop would lose.
//
// Nothing in this package ever reads the column back — it is bound by the insert
// and compared against by the lookup — so a statement projecting it would be a
// statement putting a stored credential's digest in whatever a caller did next
// with the row.
func TestRender_ProjectsNoDigest(T *testing.T) {
	T.Parallel()

	must.False(T, slices.Contains(TokenColumns, DigestColumn))

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			read := statement(t, Render(d), GetTokenByDigestQuery)

			projection, _, found := strings.Cut(read, "FROM")
			must.True(t, found)

			test.StrNotContains(t, projection, DigestColumn)

			// And what it does project is what the generated row type is built
			// from, in this order: a column added here without one added there
			// is a scan that reads a column into the wrong field.
			for _, column := range TokenColumns {
				test.StrContains(t, projection, querygen.Qualify(TokensTable, column),
					test.Sprintf("column %q", column))
			}
		})
	}
}

// TestRender_LookupKeysOnTheDigestAndTheScope pins the predicate a missing
// scope would widen into every tenant's tokens.
//
// The scope is a predicate rather than a check made on the row afterwards, so a
// token presented in the wrong tenant matches nothing and reads as absent —
// which is what it is from there.
func TestRender_LookupKeysOnTheDigestAndTheScope(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			_, where, found := strings.Cut(statement(t, Render(d), GetTokenByDigestQuery), "WHERE")
			must.True(t, found)

			test.StrContains(t, where,
				querygen.Qualify(TokensTable, DigestColumn)+" = sqlc.arg("+DigestColumn+")")
			test.StrContains(t, where,
				querygen.Qualify(TokensTable, ScopeColumn)+" = sqlc.arg("+ScopeColumn+")")

			// And on nothing else. The id is not in the key — the caller holds
			// a secret rather than a row's name — and the deadline is not
			// either; see TestRender_DecidesLivenessNowhere.
			test.StrNotContains(t, where, querygen.Qualify(TokensTable, querygen.IDColumn))
		})
	}
}

// TestRender_DecidesLivenessNowhere is the ruling this corpus was ported under,
// asserted rather than left to the shapes.
//
// The store compares the deadline in Go, against the clock it was handed. A
// predicate here would be a second copy of that comparison, free to disagree
// with Token.Live about which second a link stopped working, and free to
// collapse "expired" and "already redeemed" into one affected-row count of
// zero. The sweep is the one statement that may compare the deadline, because
// it deletes rows dead by any reading.
func TestRender_DecidesLivenessNowhere(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, named := range statements(Render(d)) {
				if named.name == SweepExpiredTokensQuery {
					continue
				}

				test.StrNotContains(t, predicates(t, named), ExpiresAtColumn,
					test.Sprintf("%s decides liveness in SQL", named.name))
			}
		})
	}
}

// TestRender_ComparesAgainstNoServerClock is the other half of the same ruling,
// and the one a later port would break by reaching for the shape every other
// expiry sweep in this module uses.
//
// expires_at is stamped by the store's own clock — now plus a TTL, from a clock
// the store was handed — so a comparison against CURRENT_TIMESTAMP would be two
// clocks deciding one row, and under a test clock that only moves when a test
// moves it the two are years apart. The horizon is bound instead.
func TestRender_ComparesAgainstNoServerClock(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			test.StrNotContains(t, rendered, querygen.NowExpression)

			sweep := statement(t, rendered, SweepExpiredTokensQuery)

			test.StrContains(t, sweep, "DELETE FROM "+TokensTable)
			test.StrContains(t, sweep,
				ExpiresAtColumn+" <= sqlc.arg("+ExpiresBeforeArg+")")

			// Every scope, which is what makes it the store's own machinery
			// rather than a read: it returns a count, not rows.
			test.StrNotContains(t, sweep, ScopeColumn)
		})
	}
}

// TestRender_RedeemGuardsOnTheRedemption pins the statement that decides single
// use.
//
// The guard is redeemed_at IS NULL rather than an equality against what the read
// saw, because "has not happened yet" is not a value a caller holds. It binds
// nothing, which is what makes it a guard: there is no argument a caller could
// leave unset that would relax it.
func TestRender_RedeemGuardsOnTheRedemption(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			redeem := statement(t, Render(d), RedeemTokenQuery)

			test.StrContains(t, redeem, "UPDATE "+TokensTable)
			test.StrContains(t, redeem, RedeemedAtColumn+" = sqlc.arg("+RedeemedAtColumn+")")
			test.StrContains(t, redeem, querygen.IDColumn+" = sqlc.arg("+querygen.IDColumn+")")
			test.StrContains(t, redeem, RedeemedAtColumn+" IS NULL")

			// One column assigned, and no stamp beside it. Redemption is this
			// row's only mutation, so a last_updated_at would be a second copy
			// of the column above it.
			test.StrNotContains(t, redeem, querygen.LastUpdatedAtColumn)
		})
	}
}

// TestRender_RevokeSparesRedeemedRows pins what a completed reset does and does
// not destroy.
//
// Redeemed rows are already unspendable, and they are what answers "this link
// has already been used" for the rest of the link's life — a revocation that
// removed them would turn a completed reset's own token into one that never
// existed.
func TestRender_RevokeSparesRedeemedRows(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			revoke := statement(t, Render(d), RevokeTokensForUserQuery)

			test.StrContains(t, revoke, "DELETE FROM "+TokensTable)
			test.StrContains(t, revoke, ScopeColumn+" = sqlc.arg("+ScopeColumn+")")
			test.StrContains(t, revoke, UserColumn+" = sqlc.arg("+UserColumn+")")
			test.StrContains(t, revoke, RedeemedAtColumn+" IS NULL")
		})
	}
}

// TestRender_InsertWritesTheDigestAndNoRedemption pins the issuance write.
//
// It is a plain INSERT rather than an upsert: a digest is a secret, and a second
// row bearing one would mean the generator produced the same token twice. The
// unique index refuses that, and this statement lets it.
func TestRender_InsertWritesTheDigestAndNoRedemption(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			insert := statement(t, Render(d), InsertTokenQuery)

			test.StrContains(t, insert, "INSERT INTO "+TokensTable)

			for _, column := range InsertColumns {
				test.StrContains(t, insert, "sqlc.arg("+column+")", test.Sprintf("column %q", column))
			}

			// A token nobody has spent yet is a row whose stamp has never been
			// written, which is what the column's absence says and what a bound
			// NULL would only restate.
			test.StrNotContains(t, insert, RedeemedAtColumn)

			// And nothing converges. A collision is a failed write.
			test.StrNotContains(t, insert, "ON CONFLICT")
			test.StrNotContains(t, insert, "ON DUPLICATE KEY")
			test.StrNotContains(t, insert, "IGNORE")
		})
	}
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

// predicates returns one statement's WHERE clause, or the empty string for the
// statement that has none.
//
// The insert is that statement, and it is why this returns a value rather than
// failing: "no predicates at all" is the right answer for it, and the assertion
// reading this wants that answer rather than a skipped case.
func predicates(t *testing.T, named namedStatement) string {
	t.Helper()

	_, where, found := strings.Cut(named.body, "WHERE")
	if !found {
		return ""
	}

	return where
}
