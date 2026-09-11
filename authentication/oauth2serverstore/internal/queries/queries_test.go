package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2serverstore/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// tables pairs each table with the column list every statement over it is
// rendered from, so an assertion about "every table" is one loop rather than
// four copies that can come to cover three.
func tables() map[string][]string {
	return map[string][]string{
		ClientsTable:       ClientColumns,
		CodesTable:         CodeColumns,
		AccessTokensTable:  AccessTokenColumns,
		RefreshTokensTable: RefreshTokenColumns,
	}
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

// TestRender_RegistersEveryTable is the registry half of the guarantee the
// canonical .sql files are the query half of.
//
// Not one of these tables gets a standard set, so nothing registers any of them
// as a side effect of emitting one. A consumer reading the registry back to
// truncate a database between integration tests would otherwise miss all four,
// and the symptom would be a different test failing later on rows the previous
// one left behind.
func TestRender_RegistersEveryTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	for table := range tables() {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

// TestTables_AreTheTablesTheDDLCreates is the cross-check between the two halves
// of "what tables does this store own": the canonical spellings here, which the
// registry and the store's prefix rendering both read, and the names the
// migrations package creates.
//
// Neither derives from the other on purpose — one is a Go constant a statement
// interpolates, the other is in the DDL — so this is where a rename in one and
// not the other stops being invisible.
func TestTables_AreTheTablesTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			for table := range tables() {
				test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+table)
			}
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

			for table, columns := range tables() {
				for _, column := range columns {
					test.StrContains(t, ddl, column, test.Sprintf("%s.%s", table, column))
				}
			}
		})
	}
}

// TestColumns_CarryNoConventionColumn pins the absences the rest of this package
// is derived from. querygen renders a statement's predicates from the column
// list it is handed, so an archived_at appearing here would silently add a
// predicate to every read and make the deletes unable to reach the rows they
// exist for, and a last_updated_at would have every guarded write stamp a column
// the schema does not have.
func TestColumns_CarryNoConventionColumn(T *testing.T) {
	T.Parallel()

	for table, columns := range tables() {
		T.Run(table, func(t *testing.T) {
			t.Parallel()

			for _, column := range []string{
				querygen.LastUpdatedAtColumn,
				querygen.ArchivedAtColumn,
				querygen.LastIndexedAtColumn,
			} {
				test.False(t, slices.Contains(columns, column), test.Sprintf("column %q", column))
			}
		})
	}
}

// TestColumns_OnlyTheRegistrationsCarryAnID is the natural-key half of the port,
// stated as the property the statements are derived from rather than as a fact
// about one table.
//
// querygen renders a single-row statement's id predicate when the column list
// has an id and not when it does not. So a credential table that grew one would
// have every read and every guard silently key on a column no caller passes,
// and the registration table that lost one would have its get answer with
// whichever row the planner reached first.
func TestColumns_OnlyTheRegistrationsCarryAnID(T *testing.T) {
	T.Parallel()

	for table, columns := range tables() {
		T.Run(table, func(t *testing.T) {
			t.Parallel()

			if table == ClientsTable {
				test.True(t, slices.Contains(columns, IDColumn))
				test.False(t, slices.Contains(columns, HashColumn))

				return
			}

			test.False(t, slices.Contains(columns, IDColumn))
			test.True(t, slices.Contains(columns, HashColumn))
		})
	}
}

// TestRender_EmitsTheStatementsTheStoreExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	want := []string{
		CreateClientQuery,
		GetClientQuery,
		DeleteClientQuery,
		SweepClientsQuery,

		CreateAuthorizationCodeQuery,
		GetAuthorizationCodeQuery,
		ConsumeAuthorizationCodeQuery,
		SweepAuthorizationCodesQuery,

		CreateAccessTokenQuery,
		GetAccessTokenQuery,
		RevokeAccessTokenQuery,
		RevokeAccessTokenFamilyQuery,
		SweepAccessTokensQuery,

		CreateRefreshTokenQuery,
		GetRefreshTokenQuery,
		ConsumeRefreshTokenQuery,
		RevokeRefreshTokenQuery,
		RevokeRefreshTokenFamilyQuery,
		SweepRefreshTokensQuery,
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			var names []string
			for _, s := range statements(Render(d)) {
				names = append(names, s.name)
			}

			test.SliceEqFunc(t, want, names, func(a, b string) bool { return a == b })

			// Nothing lists any of these tables, and nothing updates a row in
			// place: what a write here does is stamp a nullable column that was
			// NULL. The absences are what make the shape a decision rather than
			// an oversight.
			rendered := Render(d)
			test.StrNotContains(t, rendered, "ListClient")
			test.StrNotContains(t, rendered, "ORDER BY")
			test.StrNotContains(t, rendered, "archived_at")
		})
	}
}

// TestRender_AddressesEveryCredentialByItsHash is what a table with no id has
// instead of an id predicate. querygen renders none for such a column list, so a
// statement naming no Match would be a read of every row or a delete of the
// table.
func TestRender_AddressesEveryCredentialByItsHash(T *testing.T) {
	T.Parallel()

	keyed := []string{
		GetAuthorizationCodeQuery, ConsumeAuthorizationCodeQuery,
		GetAccessTokenQuery, RevokeAccessTokenQuery,
		GetRefreshTokenQuery, ConsumeRefreshTokenQuery, RevokeRefreshTokenQuery,
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range keyed {
				test.StrContains(t, statement(t, Render(d), name),
					HashColumn+" = sqlc.arg("+HashColumn+")",
					test.Sprintf("%s does not key on the hash", name))
			}
		})
	}
}

// TestRender_ConsumeIsOneGuardedUpdate is the statement the whole store is
// arranged around, so each half of its predicate is pinned rather than left to
// the renderer.
//
// A store that dropped one clause would pass every test that redeems one
// credential at a time, on every dialect, and would resolve two concurrent
// redemptions to two winners in production.
func TestRender_ConsumeIsOneGuardedUpdate(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			code := statement(t, Render(d), ConsumeAuthorizationCodeQuery)

			test.StrContains(t, code, RedeemedAtColumn+" = sqlc.arg("+RedeemedAtColumn+")")
			test.StrContains(t, code, RedeemedAtColumn+" IS NULL")
			test.StrContains(t, code, ExpiresAtColumn+" > sqlc.arg("+NowArg+")")

			refresh := statement(t, Render(d), ConsumeRefreshTokenQuery)

			test.StrContains(t, refresh, RedeemedAtColumn+" IS NULL")
			test.StrContains(t, refresh, ExpiresAtColumn+" > sqlc.arg("+NowArg+")")

			// And revoked_at IS NULL, which is what keeps a revoked token from
			// reading as a replay — otherwise every sign-out a client retried
			// would revoke the family it just ended.
			test.StrContains(t, refresh, RevokedAtColumn+" IS NULL")
		})
	}
}

// TestRender_RevocationsAreIdempotentByPredicate pins the guard that makes a
// second revocation report zero rows rather than move the timestamp, so the
// record still says when the token actually stopped working.
func TestRender_RevocationsAreIdempotentByPredicate(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range []string{
				RevokeAccessTokenQuery, RevokeAccessTokenFamilyQuery,
				RevokeRefreshTokenQuery, RevokeRefreshTokenFamilyQuery,
			} {
				test.StrContains(t, statement(t, Render(d), name), RevokedAtColumn+" IS NULL",
					test.Sprintf("%s is not guarded", name))
			}

			// The family revocations key on the family and nothing else: one
			// statement per table, so a reuse detected on a refresh token
			// revokes the access tokens it minted too.
			for _, name := range []string{RevokeAccessTokenFamilyQuery, RevokeRefreshTokenFamilyQuery} {
				test.StrContains(t, statement(t, Render(d), name),
					FamilyIDColumn+" = sqlc.arg("+FamilyIDColumn+")")
			}
		})
	}
}

// TestRender_SweepsBindTheirHorizon is the property that separates this corpus
// from one written against the server's clock.
//
// Sweep takes the instant from its caller, and the conformance suite depends on
// it: one database serves every parallel subtest, so a sweep at "whatever the
// database thinks now is" deletes another subtest's expired code out from under
// the assertion that consuming it reports ErrExpired.
func TestRender_SweepsBindTheirHorizon(T *testing.T) {
	T.Parallel()

	sweeps := []string{
		SweepClientsQuery, SweepAuthorizationCodesQuery,
		SweepAccessTokensQuery, SweepRefreshTokensQuery,
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range sweeps {
				sweep := statement(t, Render(d), name)

				test.StrContains(t, sweep, "DELETE FROM ")
				test.StrContains(t, sweep, ExpiresAtColumn+" <= sqlc.arg("+NowArg+")",
					test.Sprintf("%s does not bind its horizon", name))
				test.StrNotContains(t, sweep, querygen.NowExpression,
					test.Sprintf("%s asks the server what time it is", name))
			}

			// The registration sweep's extra predicate. Without it a
			// registration with no expiry — stored as NULL, which is what
			// "does not lapse" is written as — would be read by a comparison
			// that treats it as having lapsed at the beginning of time.
			test.StrContains(t, statement(t, Render(d), SweepClientsQuery),
				ExpiresAtColumn+" IS NOT NULL")
		})
	}
}

// TestRender_SweepsAndGuardsReadOneBoundary is why stillLive is derived from
// elapsed rather than written beside it.
//
// The rows a sweep collects and the rows a consume may spend are one boundary
// read in two directions, and there is no reading under which a row belongs to
// neither set or to both. Written separately they could come to disagree about
// the instant a deadline falls on, which is a disagreement no test of either
// statement alone can see.
func TestRender_SweepsAndGuardsReadOneBoundary(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			collected := ExpiresAtColumn + " <= sqlc.arg(" + NowArg + ")"
			spendable := ExpiresAtColumn + " > sqlc.arg(" + NowArg + ")"

			test.StrContains(t, statement(t, rendered, SweepAuthorizationCodesQuery), collected)
			test.StrContains(t, statement(t, rendered, ConsumeAuthorizationCodeQuery), spendable)

			// Neither boundary is spelled any other way anywhere in the corpus,
			// which is the property a second hand-written comparison breaks.
			test.EqOp(t, 4, strings.Count(rendered, collected))
			test.EqOp(t, 2, strings.Count(rendered, spendable))
		})
	}
}

// TestRender_CreatesSkipADuplicateRatherThanRaise pins the clause that makes
// ErrClientExists and ErrRecordExists reportable without parsing a driver's
// error. It is spelled three ways, and losing it on one dialect turns a
// duplicate into an unrecognized error there and nowhere else.
func TestRender_CreatesSkipADuplicateRatherThanRaise(T *testing.T) {
	T.Parallel()

	creates := map[string]string{
		CreateClientQuery:            IDColumn,
		CreateAuthorizationCodeQuery: HashColumn,
		CreateAccessTokenQuery:       HashColumn,
		CreateRefreshTokenQuery:      HashColumn,
	}

	T.Run("names the conflict target where the dialect takes one", func(t *testing.T) {
		t.Parallel()

		for name, key := range creates {
			created := statement(t, Render(dialect.Postgres), name)

			test.StrContains(t, created, "ON CONFLICT ("+key+") DO NOTHING",
				test.Sprintf("%s", name))
			test.StrNotContains(t, created, "IGNORE")
		}
	})

	// MySQL and SQLite spell it before INTO and name no target at all, which is
	// the same statement written the only way each accepts.
	T.Run("spells the same skip each other dialect's way", func(T *testing.T) {
		T.Parallel()

		for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite} {
			T.Run(string(d), func(t *testing.T) {
				t.Parallel()

				for name := range creates {
					created := statement(t, Render(d), name)

					test.StrContains(t, created, "IGNORE INTO ", test.Sprintf("%s", name))
					test.StrNotContains(t, created, "ON CONFLICT")
				}
			})
		}
	})

	// The registration's creation time is supplied rather than defaulted, which
	// is the one place this schema parts company with the module's convention:
	// there is no id for it to disagree with, and it is stamped by the same
	// clock as the expiry beside it.
	T.Run("the registration supplies its own creation time", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			test.StrContains(t, statement(t, Render(d), CreateClientQuery),
				"sqlc.arg("+CreatedAtColumn+")", test.Sprintf("dialect %q", d))
		}
	})
}

// TestRender_NullableStampsBindAsAbsent pins which columns a write may leave
// NULL, because NULL is a fact here rather than a missing value: it is what
// "does not lapse", "not yet redeemed" and "not yet revoked" are written as, and
// every IS NULL guard in the corpus is written against it.
func TestRender_NullableStampsBindAsAbsent(T *testing.T) {
	T.Parallel()

	nullable := map[string][]string{
		CreateClientQuery:            {ExpiresAtColumn},
		CreateAuthorizationCodeQuery: {RedeemedAtColumn},
		CreateAccessTokenQuery:       {RevokedAtColumn},
		CreateRefreshTokenQuery:      {RedeemedAtColumn, RevokedAtColumn},
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, columns := range nullable {
				created := statement(t, Render(d), name)

				for _, column := range columns {
					test.StrContains(t, created, "sqlc.narg("+column+")",
						test.Sprintf("%s does not admit a NULL %s", name, column))
				}
			}

			// A credential's own deadline never does. Everything here expires,
			// and a NULL there would be a token that outlives every sweep.
			for _, name := range []string{
				CreateAuthorizationCodeQuery, CreateAccessTokenQuery, CreateRefreshTokenQuery,
			} {
				test.StrContains(t, statement(t, Render(d), name), "sqlc.arg("+ExpiresAtColumn+")",
					test.Sprintf("%s", name))
			}
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
