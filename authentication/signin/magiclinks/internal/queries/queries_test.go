package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin/magiclinks/migrations"

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

	test.True(t, querygen.TableRegistered(LinksTable),
		test.Sprintf("%s is not registered", LinksTable))
}

// TestLinksTable_IsTheTableTheDDLCreates is the cross-check between the two
// halves of "what table does this store own": the canonical spelling here, which
// the registry and the store's prefix rendering both read, and the name the
// migrations package creates.
//
// Neither derives from the other on purpose — one is a Go constant a statement
// interpolates, the other is in the DDL — so this is where a rename in one and
// not the other stops being invisible.
func TestLinksTable_IsTheTableTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+LinksTable)
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

// TestColumns_CarryNoConventionTriple pins the absences the rest of this package
// is derived from.
//
// querygen renders a statement's predicates from the column list it is handed, so
// an archived_at appearing here would silently add a predicate to every read —
// and would make the sweep and the revocation the writes unable to reach the
// rows they exist for. last_updated_at is the other one, and it is why the
// redemption assigns one column rather than two: querygen stamps that column
// wherever a column list carries it.
//
// There is no id either, and that one is not an absence querygen cares about but
// a fact every predicate in this corpus rests on: the key is the digest of a
// credential, so a statement handed this list renders no id predicate and every
// WHERE clause here is one the statement named.
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
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	want := []string{
		InsertLinkQuery,
		GetLinkQuery,
		RedeemLinkQuery,
		RevokeLinksForSubjectQuery,
		SweepLinksQuery,
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

			// Nothing archives one of these rows, and nothing lists them: the
			// only way to name one is to hold the token it was minted from, so
			// there is no cursor, no filter window and no descending variant.
			test.StrNotContains(t, rendered, querygen.ArchivedAtColumn)
			test.StrNotContains(t, rendered, "page_cursor")
			test.StrNotContains(t, rendered, "result_limit")
			test.StrNotContains(t, rendered, "filtered_count")
			test.StrNotContains(t, rendered, querygen.DescendingSuffix)
		})
	}
}

// TestRender_ProjectsNoHash is the property the whole projection exists for, and
// the one a SELECT * with a Go-side drop would lose.
//
// Nothing in this package ever reads the column back — it is bound by the insert
// and compared against by the read and the redemption — so a statement projecting
// it would put a stored credential's digest in whatever a caller did next with
// the row.
func TestRender_ProjectsNoHash(T *testing.T) {
	T.Parallel()

	must.False(T, slices.Contains(RecordColumns, HashColumn))

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			read := statement(t, Render(d), GetLinkQuery)

			projection, _, found := strings.Cut(read, "FROM")
			must.True(t, found)

			test.StrNotContains(t, projection, HashColumn)

			// And what it does project is what the generated row type is built
			// from, in this order: a column added here without one added there
			// is a scan that reads a column into the wrong field.
			for _, column := range RecordColumns {
				test.StrContains(t, projection, querygen.Qualify(LinksTable, column),
					test.Sprintf("column %q", column))
			}
		})
	}
}

// TestRender_EveryStatementButTheSweepIsScoped pins the tenancy obligation the
// module's rule is easiest to miss on: not "a scoped variant exists" but "no
// statement omits it".
//
// The sweep is the one exception and it is the enumerated kind: it names no
// rows, returns a count rather than data, and is the store's own machinery
// running on a timer rather than anybody's read.
func TestRender_EveryStatementButTheSweepIsScoped(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, named := range statements(Render(d)) {
				if named.name == SweepLinksQuery {
					test.StrNotContains(t, named.body, ScopeColumn,
						test.Sprintf("%s is scoped", named.name))

					continue
				}

				test.StrContains(t, named.body, ScopeColumn,
					test.Sprintf("%s omits the scope", named.name))
			}
		})
	}
}

// TestRender_RedemptionRepeatsEveryRowStateTestItsAnswerRestsOn is the statement
// this store exists for, and the lesson outbox's lease mode paid for.
//
// Its affected-row count is what decides who spent the link, so every fact the
// answer depends on has to be tested by the write itself, at the instant the row
// changes — not by a caller a round trip earlier. The two stamp guards bind
// nothing, which is what makes them guards: there is no argument a caller could
// leave unset to relax one.
func TestRender_RedemptionRepeatsEveryRowStateTestItsAnswerRestsOn(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			redeem := statement(t, Render(d), RedeemLinkQuery)

			test.StrContains(t, redeem, "UPDATE "+LinksTable)
			test.StrContains(t, redeem, RedeemedAtColumn+" = sqlc.arg("+RedeemedAtColumn+")")
			test.StrContains(t, redeem, HashColumn+" = sqlc.arg("+HashColumn+")")
			test.StrContains(t, redeem, ScopeColumn+" = sqlc.arg("+ScopeColumn+")")
			test.StrContains(t, redeem, RedeemedAtColumn+" IS NULL")
			test.StrContains(t, redeem, RevokedAtColumn+" IS NULL")
			test.StrContains(t, redeem, ExpiresAtColumn+" > sqlc.arg("+NowArg+")")

			// One column assigned, and no stamp beside it. A last_updated_at
			// would be a second record of the one mutation this write makes.
			test.StrNotContains(t, redeem, querygen.LastUpdatedAtColumn)
		})
	}
}

// TestRender_RevocationIsKeyedOnTheSubjectAlone pins the one write in this
// corpus that names rows it was not handed.
//
// There is no per-link revocation beside it and there cannot usefully be one: a
// row is addressed by the digest of a credential nobody but the recipient holds,
// so a caller wanting to withdraw somebody's outstanding links has a subject and
// nothing to loop over. That is where this parts company with the refresh token
// corpus, which carries two revocations because a family is a thing a caller can
// hold the identifier for.
func TestRender_RevocationIsKeyedOnTheSubjectAlone(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			revoke := statement(t, Render(d), RevokeLinksForSubjectQuery)

			test.StrContains(t, revoke, "UPDATE "+LinksTable)
			test.StrContains(t, revoke, RevokedAtColumn+" = sqlc.arg("+RevokedAtColumn+")")
			test.StrContains(t, revoke, ScopeColumn+" = sqlc.arg("+ScopeColumn+")")
			test.StrContains(t, revoke, SubjectIDColumn+" = sqlc.arg("+SubjectIDColumn+")")

			// The guard that makes revoking idempotent: a second call matches
			// nothing and reports zero rather than moving the stamp, so the
			// record still says when the links actually stopped working.
			test.StrContains(t, revoke, RevokedAtColumn+" IS NULL")

			// And no liveness predicate. A link that lapsed on its own and one
			// somebody withdrew both end up revoked, which after an account is
			// disabled is the more useful of the two true sentences.
			test.StrNotContains(t, revoke, ExpiresAtColumn)

			// It is not keyed on the hash, which is the half that says this is a
			// revocation rather than a second spelling of the redemption.
			test.StrNotContains(t, revoke, HashColumn)
		})
	}
}

// TestRender_SweepsOnThePurgeDeadlineAndNotTheExpiry is the retention ruling,
// asserted rather than left to the shapes.
//
// A row collected at its own expiry can no longer be told from a row that never
// existed, and only one of those two stories is one an operator can act on:
// "this link was already used" and "no such link" are the same dead end for the
// bearer and different sentences in a support queue. Neither reaches the caller
// — see the store's Redeem — so the deadline buys a span rather than a refusal,
// and it is the only column the sweep reads.
func TestRender_SweepsOnThePurgeDeadlineAndNotTheExpiry(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			sweep := statement(t, Render(d), SweepLinksQuery)

			test.StrContains(t, sweep, "DELETE FROM "+LinksTable)
			test.StrContains(t, sweep, PurgeAfterColumn+" <= sqlc.arg("+PurgeBeforeArg+")")
			test.StrNotContains(t, sweep, ExpiresAtColumn)
		})
	}
}

// TestRender_ComparesAgainstNoServerClock is the ruling both clock comparisons
// in this corpus are written under.
//
// expires_at and purge_after are stamped by the store's own clock, so a
// comparison against CURRENT_TIMESTAMP would be two clocks deciding one row —
// and under a test clock that only moves when a test moves it the two are years
// apart. Both horizons are bound instead.
func TestRender_ComparesAgainstNoServerClock(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.StrNotContains(t, Render(d), querygen.NowExpression)
		})
	}
}

// TestRender_InsertWritesNeitherStamp pins the mint write.
//
// It is a plain INSERT rather than an upsert or an insert-ignore: the hash is the
// digest of a token, so a second row bearing one would mean the generator
// produced the same token twice. The primary key refuses that and this statement
// lets it — a mint failing loudly is the correct outcome of randomness that has
// stopped being random, where an ignore would hand a caller a link that redeems
// somebody else's row.
func TestRender_InsertWritesNeitherStamp(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			insert := statement(t, Render(d), InsertLinkQuery)

			test.StrContains(t, insert, "INSERT INTO "+LinksTable)

			for _, column := range InsertColumns {
				test.StrContains(t, insert, "sqlc.arg("+column+")", test.Sprintf("column %q", column))
			}

			// A link nobody has followed or withdrawn yet is a row whose stamps
			// have never been written, which is what their absence says and what
			// a bound NULL would only restate.
			test.StrNotContains(t, insert, RedeemedAtColumn)
			test.StrNotContains(t, insert, RevokedAtColumn)

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
// assertion about one is not an assertion about whatever else happens to contain
// the same substring.
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
