package queries

import (
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// TestRender_RegistersTheTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// The table takes a standard set, so StandardCRUD registers it on its own —
// which is exactly why this is worth pinning. The day it stops taking one, a
// consumer reading the registry back to truncate a database between integration
// tests would leave this table's rows behind, and the symptom would be a
// different test failing later.
func TestRender_RegistersTheTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	for _, table := range TableNames {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

// TestTableNames_AreTheTablesTheDDLCreates is the cross-check between the two
// halves of "what tables does oauth2clients own": the canonical spelling here,
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

// TestRegisteredClientsTable_IsNotTheAuthorizationServersTable is the collision
// this package's whole naming decision exists to avoid.
//
// authentication/oauth2serverstore creates a table called oauth2_clients for
// the anonymous RFC 7591 registrations its /register endpoint writes. A
// deployment runs both schemas, both are CREATE TABLE IF NOT EXISTS, and two
// tables of one name would leave the second migration a silent no-op followed by
// a store selecting columns that are not there.
func TestRegisteredClientsTable_IsNotTheAuthorizationServersTable(t *testing.T) {
	t.Parallel()

	test.NotEq(t, "oauth2_clients", RegisteredClientsTable)
	test.EqOp(t, "oauth2_registered_clients", RegisteredClientsTable)
}

// TestRender_MatchesTheCommittedFiles is the regeneration gate, run locally
// rather than only in CI.
//
// The .sql files are what sqlc is run over and what sqlc-gen-unison generates the
// querier from, and the whole value of running either is that they are the
// statements the store executes. A hand-edit to one — or a column list changed
// without regenerating — would leave sqlc checking SQL nobody runs.
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

// TestRender_ScopeIsInEveryConsumerReachableStatement is the tenancy obligation,
// checked rather than promised.
//
// There is exactly one exception, and it is not a read that omits the scope:
// GetRegisteredClientByClientID *resolves* one. client_id is server-minted and
// globally unique, and the row it finds is the only thing in the system that
// knows which registry the client is in.
//
// Everything else binds it, the archive's read-back included — that read has a
// scope and it is the one the archive itself bound, so leaving it off would
// widen a read-back past the registry the write was confined to.
//
// A statement that stopped binding it would be a read crossing registries with
// nothing reporting it.
func TestRender_ScopeIsInEveryConsumerReachableStatement(T *testing.T) {
	T.Parallel()

	unscoped := map[string]bool{
		"GetRegisteredClientByClientID": true,
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, statement := range statements(t, d) {
				if unscoped[name] {
					continue
				}

				test.StrContains(t, statement, ScopeColumn,
					test.Sprintf("%s does not bind the scope", name))
			}
		})
	}
}

// TestRender_TheLookupSeesArchivedRows is what makes the authorization server's
// lookup able to refuse a withdrawn registration by name.
//
// A predicate filtering archived rows would turn "this client was withdrawn"
// into "no such client", and the refusal would then be decided by the statement
// rather than by the store — which is the one place that reads the column and
// says what it means. The column is still projected, which is what the store
// reads to make that call.
func TestRender_TheLookupSeesArchivedRows(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			lookup, ok := statements(t, d)["GetRegisteredClientByClientID"]
			must.True(t, ok, must.Sprint("GetRegisteredClientByClientID was not emitted"))

			test.StrNotContains(t, lookup, querygen.ArchivedAtColumn+" IS NULL",
				test.Sprint("the lookup filters archived rows and cannot refuse one by name"))
			test.StrContains(t, lookup, querygen.ArchivedAtColumn,
				test.Sprint("the lookup does not project archived_at"))

			// It keys on the client_id and on nothing else. An id predicate here
			// would be a statement keyed on two things, only one of which the
			// authorization server holds.
			test.StrContains(t, lookup, ClientIDColumn+" = ")
		})
	}
}

// TestRender_CreateReportsACollisionRatherThanRaising is the portability
// property the standard insert would have cost.
//
// A raised unique-constraint violation means every backing store parsing a
// dialect's own SQLSTATE to tell "this identifier is taken" from "the database
// is broken", and the three engines spell that three ways. Zero affected rows
// says it once. The three spellings of "do not raise" are asserted per dialect
// because that is the level they differ at.
func TestRender_CreateReportsACollisionRatherThanRaising(T *testing.T) {
	T.Parallel()

	ignoring := map[dialect.Dialect]string{
		dialect.Postgres: "ON CONFLICT (" + ClientIDColumn + ") DO NOTHING",
		dialect.MySQL:    "INSERT IGNORE INTO",
		dialect.SQLite:   "INSERT OR IGNORE INTO",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			create, ok := statements(t, d)["CreateRegisteredClient"]
			must.True(t, ok, must.Sprint("CreateRegisteredClient was not emitted"))

			test.StrContains(t, create, ignoring[d],
				test.Sprintf("%s's create raises on a taken client_id", d))

			// created_at is database-owned — the schema gives it a DEFAULT — so
			// the create does not supply it and the store reads it back.
			test.StrNotContains(t, create, querygen.CreatedAtColumn,
				test.Sprint("the create supplies a creation time the database owns"))
		})
	}
}

// TestRender_UpdateAssignsNoneOfTheImmutableColumns is the three-column claim
// options() makes, checked against the statement rather than against the option.
//
// The owner, because a row that can reassign its own owner makes every ownership
// check a formality. The client_id, because it is the value every live token was
// minted against. The digest, because rotation is a call that hands back a new
// secret rather than an UPDATE nobody sees.
func TestRender_UpdateAssignsNoneOfTheImmutableColumns(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			update, ok := statements(t, d)["UpdateRegisteredClient"]
			must.True(t, ok, must.Sprint("UpdateRegisteredClient was not emitted"))

			assignments, _, found := strings.Cut(update, "WHERE")
			must.True(t, found, must.Sprintf("unparsable update %q", update))

			for _, column := range []string{BelongsToUserColumn, ClientIDColumn, SecretHashColumn} {
				test.StrNotContains(t, assignments, column+" = ",
					test.Sprintf("the update assigns %s", column))
			}

			// The four it does assign are the ones revising a registration is
			// for; the alternative to fixing a mistyped redirect URI is
			// archiving the row and invalidating every token the client holds.
			for _, column := range []string{NameColumn, DescriptionColumn, RedirectURIsColumn, ScopesColumn} {
				test.StrContains(t, assignments, column+" = ",
					test.Sprintf("the update does not assign %s", column))
			}
		})
	}
}

// TestRender_TheOwnerPageBindsBothKeys is what keeps the self-service page from
// being the administered one.
//
// The standard list's ownership column is already spent on the scope, so this
// pair is a second read rather than an argument on the first — and it has to
// bind the owner as well, or a person's own page is every registration in their
// registry.
func TestRender_TheOwnerPageBindsBothKeys(T *testing.T) {
	T.Parallel()

	// Both directions. A corpus carrying only the ascending half answers
	// sortBy=desc with an ascending page, silently.
	names := []string{"ListRegisteredClientsForOwner", "ListRegisteredClientsForOwnerDescending"}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := statements(t, d)

			for _, name := range names {
				statement, ok := rendered[name]
				must.True(t, ok, must.Sprintf("%s was not emitted", name))

				test.StrContains(t, statement, ScopeColumn+" = ",
					test.Sprintf("%s does not bind the scope", name))
				test.StrContains(t, statement, BelongsToUserColumn+" = ",
					test.Sprintf("%s does not bind the owner", name))
			}
		})
	}
}

// TestRender_EveryPageIsEmittedInBothDirections is the same property for the
// administered page, and the reason it is a separate assertion is that the two
// pages are emitted by two different calls.
func TestRender_EveryPageIsEmittedInBothDirections(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := statements(t, d)

			for _, name := range []string{"ListRegisteredClients", "ListRegisteredClientsForOwner"} {
				_, ascending := rendered[name]
				test.True(t, ascending, test.Sprintf("%s was not emitted", name))

				_, descending := rendered[name+"Descending"]
				test.True(t, descending, test.Sprintf("%sDescending was not emitted", name))
			}
		})
	}
}

// TestRender_OmitsTheStandardExistenceCheckAndCreate pins the two omissions
// options() declares, from the other side.
//
// The existence check, because nothing asks whether a registration exists
// without also wanting to read it — so emitting one would leave a generated
// method nobody calls beside a read path that answers with less scoping than the
// caller expects. The standard create, because it is replaced by the
// insert-ignore above.
func TestRender_OmitsTheStandardExistenceCheckAndCreate(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := statements(t, d)

			_, exists := rendered["RegisteredClientExists"]
			test.False(t, exists, test.Sprint("an existence check nobody calls was emitted"))

			// One create, and it is the ignoring one. Two would mean a caller
			// could reach the raising one by name.
			create, ok := rendered["CreateRegisteredClient"]
			must.True(t, ok)
			test.StrNotContains(t, create, "RETURNING")
		})
	}
}

// TestRender_EmitsTheGeneratedSetAndTheAuthoredStatements is the whole corpus,
// named, and it is what keeps this package's two descriptions of itself from
// drifting apart again.
//
// The failure it exists for is not a missing statement — TestRender_MatchesTheCommittedFiles
// and the store's own compilation would both catch that. It is an extra one. A
// reader who concluded this table authors everything at the statement would
// hand-write a get, a page or an archive the standard set already emits, and
// nothing else here would object: an authored statement compiles, renders and
// regenerates, and the symptom is a second read path answering with different
// scoping than the first.
func TestRender_EmitsTheGeneratedSetAndTheAuthoredStatements(T *testing.T) {
	T.Parallel()

	// What StandardCRUD emits: the standard set less the existence check and
	// the create that options() omits, and less nothing else.
	generated := []string{
		"ArchiveRegisteredClient",
		"GetRegisteredClient",
		"ListRegisteredClients",
		"ListRegisteredClientsDescending",
		"UpdateRegisteredClient",
	}

	// What is written out beside it: the insert-ignore that replaces the
	// standard create, the archive's read-back of the row it withdrew, the
	// authorization server's lookup, and the self-service page in both
	// directions.
	authored := []string{
		"CreateRegisteredClient",
		"GetArchivedRegisteredClient",
		"GetRegisteredClientByClientID",
		"ListRegisteredClientsForOwner",
		"ListRegisteredClientsForOwnerDescending",
	}

	expected := slices.Sorted(slices.Values(slices.Concat(generated, authored)))

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.Eq(t, expected, slices.Sorted(maps.Keys(statements(t, d))))
		})
	}
}

// TestRender_TheArchiveReadBackSeesOnlyWithdrawnRows pins the complement, which
// is the one statement in this corpus rendered from no column list and the only
// one carrying archived_at IS NOT NULL.
//
// It is what makes the read-back an assertion rather than a second lookup. The
// row an archive has just moved is the one row every other keyed statement over
// this table is written not to return, so a read-back carrying the ordinary
// predicate would find nothing on the write it was called to describe. The
// inverse matters as much: a read-back carrying no archived predicate at all
// would answer a guard that matched nothing with a live row.
func TestRender_TheArchiveReadBackSeesOnlyWithdrawnRows(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			readBack, ok := statements(t, d)["GetArchivedRegisteredClient"]
			must.True(t, ok, must.Sprint("GetArchivedRegisteredClient was not emitted"))

			test.StrContains(t, readBack, querygen.ArchivedAtColumn+" IS NOT NULL")
			test.StrNotContains(t, readBack, querygen.ArchivedAtColumn+" IS NULL",
				test.Sprint("the archive read-back cannot see the row it was called to describe"))
			test.StrContains(t, readBack, ScopeColumn+" = ")

			// It projects the whole table, so the row it returns converts to the
			// console read's rather than needing a converter of its own.
			for _, column := range RegisteredClients.Columns {
				test.StrContains(t, readBack, RegisteredClientsTable+"."+column,
					test.Sprintf("the archive read-back omits %s", column))
			}
		})
	}
}

// TestRender_TheConsoleReadSkipsWithdrawnRows is the other side of it: the
// read-back is the exception, and it is one.
//
// It is also what makes the create's and the update's read-back this statement
// rather than one of their own — neither write withdraws the row, so the read
// every console makes reaches both on the transaction that wrote them.
func TestRender_TheConsoleReadSkipsWithdrawnRows(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			body, ok := statements(t, d)["GetRegisteredClient"]
			must.True(t, ok, must.Sprint("GetRegisteredClient was not emitted"))

			test.StrContains(t, body, querygen.ArchivedAtColumn+" IS NULL",
				test.Sprint("a withdrawn registration is absent from the console read"))
		})
	}
}

// TestTable_ColumnsAreDeclaredOnce is the drift check between the column list
// and the DDL it projects.
//
// The scope, the id and the three timestamp columns all have to be there,
// because every statement this package emits derives a predicate or a projection
// from one of them.
func TestTable_ColumnsAreDeclaredOnce(t *testing.T) {
	t.Parallel()

	required := []string{
		querygen.IDColumn,
		ScopeColumn,
		BelongsToUserColumn,
		ClientIDColumn,
		SecretHashColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	}

	for _, column := range required {
		test.True(t, slices.Contains(RegisteredClients.Columns, column),
			test.Sprintf("the table does not declare %s", column))
	}

	test.EqOp(t, len(RegisteredClients.Columns),
		len(slices.Compact(slices.Sorted(slices.Values(RegisteredClients.Columns)))),
		test.Sprint("the table declares a column twice"))
}

// TestTable_InsertColumnsLeaveTheDatabaseOwnedOnesOut is why the store reads
// created_at back rather than supplying it.
//
// A caller-supplied creation time is how a row ends up with one that disagrees
// with its id, and the cursor walk orders by id while the filter window compares
// created_at.
func TestTable_InsertColumnsLeaveTheDatabaseOwnedOnesOut(t *testing.T) {
	t.Parallel()

	insert := RegisteredClients.InsertColumns()

	for _, column := range []string{
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	} {
		test.False(t, slices.Contains(insert, column),
			test.Sprintf("the create supplies %s", column))
	}

	// Everything else is written by the caller, the scope included: it is a
	// column, not a convention, so the write names it.
	test.True(t, slices.Contains(insert, ScopeColumn))
	test.True(t, slices.Contains(insert, BelongsToUserColumn))
}

// TestTable_ColumnsExceptDropsOnlyWhatItNames is the mechanism the two keyed
// reads are rendered through: querygen derives a statement's predicates from the
// column list it is handed, so leaving a column out is how a statement says it
// does not key on it.
//
// What comes back is a separate list, which is why the lookup still projects the
// id and archived_at it drops here.
func TestTable_ColumnsExceptDropsOnlyWhatItNames(t *testing.T) {
	t.Parallel()

	kept := RegisteredClients.ColumnsExcept(querygen.IDColumn, querygen.ArchivedAtColumn)

	test.False(t, slices.Contains(kept, querygen.IDColumn))
	test.False(t, slices.Contains(kept, querygen.ArchivedAtColumn))
	test.EqOp(t, len(RegisteredClients.Columns)-2, len(kept))

	// Order is preserved, because the generated parameter structs follow the
	// projection order rather than the order somebody happened to exclude in.
	test.True(t, slices.IsSorted(indicesIn(RegisteredClients.Columns, kept)))

	// Naming nothing drops nothing.
	test.Eq(t, RegisteredClients.Columns, RegisteredClients.ColumnsExcept())
}

// TestFileName_NamesTheGeneratedFileInThePath keeps the _generated suffix where
// a reviewer sees it.
//
// A path is what shows up in a diff, what CI's glob selects, and what a reader
// scanning this directory reads first — and these are the files whose answer to
// "this line is wrong" is to edit something else.
func TestFileName_NamesTheGeneratedFileInThePath(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		name := FileName(d)

		test.StrHasPrefix(t, string(d), name)
		test.StrHasSuffix(t, "_generated.sql", name)
	}
}

// indicesIn reports where each of subset's entries sits in whole, so that an
// assertion about preserved order is about the order rather than about a
// particular column list.
func indicesIn(whole, subset []string) []int {
	out := make([]int, 0, len(subset))
	for _, column := range subset {
		out = append(out, slices.Index(whole, column))
	}

	return out
}

// statements splits one dialect's rendered corpus into its named statements.
func statements(tb testing.TB, d dialect.Dialect) map[string]string {
	tb.Helper()

	rendered := map[string]string{}

	for block := range strings.SplitSeq(Render(d), "-- name: ") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}

		name, _, found := strings.Cut(block, " ")
		must.True(tb, found, must.Sprintf("unparsable statement block %q", block))

		rendered[name] = block
	}

	return rendered
}
