package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/links/database/migrations"

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
// This table gets no standard set — nothing lists these rows, because the only
// way to name one is to hold the token it was minted from — so nothing registers
// it as a side effect of emitting one. A consumer reading the registry back to
// truncate a database between integration tests would otherwise miss it.
func TestRender_RegistersTheTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	test.True(t, querygen.TableRegistered(LinksTable),
		test.Sprintf("%s is not registered", LinksTable))
}

// TestLinksTable_IsTheTableTheDDLCreates is the cross-check between the two
// halves of "what table does this store own": the canonical spelling here,
// which the registry and the store's prefix rendering both read, and the name
// the migrations package creates.
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

// TestColumns_CarryNoSoftDelete pins the absences the rest of this package is
// derived from. querygen renders a statement's predicates from the column list
// it is handed, so an archived_at appearing here would silently add a predicate
// to every read — and would make the sweep the one write unable to reach the
// rows it exists for.
//
// last_updated_at is the other one, and it is why the resolution assigns three
// columns rather than four: querygen stamps that column wherever a column list
// carries it, and a stamp beside resolved_at would be a second record of the one
// mutation this row has.
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

// TestUnkeyedColumns_AreTheTableWithoutItsID pins the idiom the sweep depends
// on. querygen renders the id predicate from the column list, so a list that
// grew one back would silently narrow the sweep to a single row nobody named.
func TestUnkeyedColumns_AreTheTableWithoutItsID(t *testing.T) {
	t.Parallel()

	test.False(t, slices.Contains(unkeyedColumns, querygen.IDColumn))
	test.EqOp(t, len(Columns)-1, len(unkeyedColumns))

	for _, column := range Columns {
		if column == querygen.IDColumn {
			continue
		}

		test.True(t, slices.Contains(unkeyedColumns, column), test.Sprintf("column %q", column))
	}
}

// TestRender_EmitsTheStatementsTheStoreExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	want := []string{InsertLinkQuery, GetLinkQuery, ResolveLinkQuery, RevokeSubjectLinksQuery, SweepLinksQuery}

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

			// Nothing lists these rows and nothing archives one: a link is
			// written once, read by its digest, stamped once, and deleted. The
			// absences are what make the table's shape a decision rather than
			// an oversight.
			test.StrNotContains(t, rendered, "ListLink")
			test.StrNotContains(t, rendered, querygen.ArchivedAtColumn)
		})
	}
}

// TestRender_ReadProjectsTheRowWithoutItsID pins what a Get hands back.
//
// The id is what the caller asked with — the digest of the token they hold — so
// projecting it would return the argument. What it does project is what the
// generated row type is built from, in this order: a column added here without
// one added there is a scan that reads a column into the wrong field.
func TestRender_ReadProjectsTheRowWithoutItsID(T *testing.T) {
	T.Parallel()

	must.False(T, slices.Contains(RecordColumns, querygen.IDColumn))

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			read := statement(t, Render(d), GetLinkQuery)

			projection, _, found := strings.Cut(read, "FROM")
			must.True(t, found)

			for _, column := range RecordColumns {
				test.StrContains(t, projection, querygen.Qualify(LinksTable, column),
					test.Sprintf("column %q", column))
			}

			// And it keys on the id alone: neither the deadline nor the state
			// is a predicate here, because both are the Minter's to decide.
			_, where, ok := strings.Cut(read, "WHERE")
			must.True(t, ok)

			test.StrContains(t, where,
				querygen.Qualify(LinksTable, querygen.IDColumn)+" = sqlc.arg("+querygen.IDColumn+")")
			test.StrNotContains(t, where, StateColumn)
		})
	}
}

// TestRender_DecidesLivenessNowhere is the ruling this corpus was written
// under, asserted rather than left to the shapes.
//
// links.Record.Usable compares the deadline in Go, against the Minter's clock,
// so that liveness is decided in one place above the store rather than by
// whichever engine is answering. A predicate here would be a second copy of it, free
// to disagree with Inspect about which second a link stopped working, and free
// to collapse "expired" and "already resolved" into one affected-row count of
// zero. The sweep is the one statement that may compare a deadline, because it
// deletes rows dead by any reading.
func TestRender_DecidesLivenessNowhere(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, named := range statements(Render(d)) {
				if named.name == SweepLinksQuery {
					continue
				}

				test.StrNotContains(t, predicates(t, named), ExpiresAtColumn,
					test.Sprintf("%s decides liveness in SQL", named.name))
			}
		})
	}
}

// TestRender_ComparesAgainstNoServerClock is the other half of the same ruling,
// and the one a later change would break by reaching for the shape every other
// expiry sweep in this module uses.
//
// purge_after is stamped by the Minter's own clock — an expiry plus a retention
// window — so a comparison against CURRENT_TIMESTAMP would be two clocks
// deciding one row, and under a test clock that only moves when a test moves it
// the two are years apart. The horizon is bound instead.
func TestRender_ComparesAgainstNoServerClock(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			test.StrNotContains(t, rendered, querygen.NowExpression)

			sweep := statement(t, rendered, SweepLinksQuery)

			test.StrContains(t, sweep, "DELETE FROM "+LinksTable)
			test.StrContains(t, sweep, PurgeAfterColumn+" <= sqlc.arg("+PurgeBeforeArg+")")

			// It collects on the purge deadline rather than on the expiry,
			// which is the whole retention policy: a spent link keeps answering
			// "already used" for exactly as long as the Minter said it should.
			test.StrNotContains(t, sweep, ExpiresAtColumn)
		})
	}
}

// TestRender_ResolveGuardsOnTheResolution pins the statement that decides
// single use, and with it the reason this store needs no lock service.
//
// The guard is resolved_at IS NULL rather than an equality against the state
// the read saw, because "has not happened yet" is not a value a caller holds. It
// binds nothing, which is what makes it a guard: there is no argument a caller
// could leave unset that would relax it.
func TestRender_ResolveGuardsOnTheResolution(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			resolve := statement(t, Render(d), ResolveLinkQuery)

			test.StrContains(t, resolve, "UPDATE "+LinksTable)
			test.StrContains(t, resolve, querygen.IDColumn+" = sqlc.arg("+querygen.IDColumn+")")
			test.StrContains(t, resolve, ResolvedAtColumn+" IS NULL")

			for _, column := range ResolveColumns {
				test.StrContains(t, resolve, column+" = sqlc.",
					test.Sprintf("column %q", column))
			}

			// Nothing else is assigned. A resolution that could move what a
			// link was bound to, when it was minted, or when it stopped being
			// redeemable would be one nobody could reason about afterwards.
			for _, column := range []string{ActionColumn, SubjectColumn, ExpiresAtColumn, querygen.CreatedAtColumn} {
				test.StrNotContains(t, resolve, column+" = sqlc.",
					test.Sprintf("column %q", column))
			}

			// And no stamp beside resolved_at: resolution is this row's only
			// mutation, so a last_updated_at would be a second copy of it.
			test.StrNotContains(t, resolve, querygen.LastUpdatedAtColumn)
		})
	}
}

// TestRender_RevokeForSubjectKeysOnTheSubjectAlone pins the plural revoke.
//
// It is the one statement here that moves rows it cannot name in advance and
// still writes rather than deletes, so three things about it are load-bearing:
// it keys on the subject, it carries the resolution's own guard so a redemption
// of one of the same links cannot also win, and it keys on nothing else — no
// id, no action, and no scope, because the caller reaching for it knows the
// person and not the links.
func TestRender_RevokeForSubjectKeysOnTheSubjectAlone(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			revoke := statement(t, Render(d), RevokeSubjectLinksQuery)

			test.StrContains(t, revoke, "UPDATE "+LinksTable)
			test.StrContains(t, revoke, SubjectColumn+" = sqlc.arg("+SubjectColumn+")")
			test.StrContains(t, revoke, ResolvedAtColumn+" IS NULL")

			// The same three columns the single-link resolution assigns, from
			// the same list. Two spellings of "what a resolution assigns" is
			// how the two writes would come to leave a row in two shapes.
			for _, column := range ResolveColumns {
				test.StrContains(t, revoke, column+" = sqlc.",
					test.Sprintf("column %q", column))
			}

			// No id predicate. The statement's whole point is that the caller
			// does not have one.
			test.StrNotContains(t, revoke, querygen.IDColumn+" = sqlc.arg(")

			// And no action predicate: an operator revoking after a suspected
			// compromise does not know what was minted.
			test.StrNotContains(t, revoke, ActionColumn+" = sqlc.arg(")
		})
	}
}

// TestRender_InsertConvergesOnNothing pins the mint write.
//
// The id is the digest of the token, so a second row bearing one would mean the
// generator repeated itself. The primary key refuses that and this statement
// lets it: an ignore or an upsert would hand the second caller a URL that
// redeems the first caller's link.
func TestRender_InsertConvergesOnNothing(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			insert := statement(t, Render(d), InsertLinkQuery)

			test.StrContains(t, insert, "INSERT INTO "+LinksTable)

			for _, column := range Columns {
				if slices.Contains(NullableColumns, column) {
					test.StrContains(t, insert, "sqlc.narg("+column+")", test.Sprintf("column %q", column))

					continue
				}

				test.StrContains(t, insert, "sqlc.arg("+column+")", test.Sprintf("column %q", column))
			}

			test.StrNotContains(t, insert, "ON CONFLICT")
			test.StrNotContains(t, insert, "ON DUPLICATE KEY")
			test.StrNotContains(t, insert, "IGNORE")
		})
	}
}

// TestRender_BindsNoTenancyScope pins the absence of a scope column, so that it
// stays a decision somebody reads rather than an omission somebody corrects.
//
// The module's tenancy rule guards against a read that forgot the scope and so
// matched everything. This corpus has no read that can widen: every statement
// is keyed by a primary key that is a digest of thirty-two bytes of randomness.
// The enumerating reads a scope would serve want the subject instead, which is
// a column here already. See migrations/postgres.sql for the argument in full.
func TestRender_BindsNoTenancyScope(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.StrNotContains(t, Render(d), "scope")
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
