package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/waitlists/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// allTables is every table declared here as a [Table], emitted or not.
var allTables = []*Table{&Lists, &Signups}

// TestRender_RegistersEveryTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// querygen.Generator.StandardCRUD registers what it emits for, which here is one
// table of two — so a consumer reading the registry back to truncate a database
// between integration tests would leave the signups behind, and the symptom
// would be a different test failing later on rows the previous one left.
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
// halves of "what tables does waitlists own": the canonical spelling here, which
// the registry and the store's prefix rendering both read, and the list
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
// The .sql files are what sqlc is run over and what sqlc-gen-unison generates
// the querier from, and the whole value of running either is that they are the
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
		"CreateList", "GetList", "ListLists", "ListListsDescending", "UpdateList", "ArchiveList",
		"GetListCreatedAt", "GetSignupCreatedAt",
		"ListOpenLists", "ListOpenListsDescending",
		"GetSignup", "GetSignupByContactDigest",
		"ListSignups", "ListSignupsDescending",
		"ListSignupsForSubject", "ListSignupsForSubjectDescending",
		"InsertSignup", "UpdateSignupNotes", "TransitionSignup", "WithdrawSignup", "ArchiveSignup",
		"WithdrawSignupsForSubject",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			test.SliceEqFunc(t, want, statementNames(rendered), func(a, b string) bool { return a == b })

			// The signups table gets no standard query: every one of them would
			// key on the id alone, and a signup is addressed by its list too.
			test.StrNotContains(t, rendered, "CreateSignup")
			test.StrNotContains(t, rendered, "UpdateSignup ")
			test.EqOp(t, 1, strings.Count(rendered, "INSERT INTO "+SignupsTable))

			// Nothing here checks a row's existence without reading it: the
			// question every signup begins with is when the list closes, which
			// an existence check cannot answer.
			test.StrNotContains(t, rendered, "Existence")
		})
	}
}

// TestRender_ScopeIsInEveryStatement is the tenancy doctrine's third obligation,
// checked rather than remembered: no read path omits the scope.
func TestRender_ScopeIsInEveryStatement(T *testing.T) {
	T.Parallel()

	// The two exceptions, and neither is a read a caller reaches. Each is the
	// read-back of the creation time a create's own INSERT just caused, by the
	// id that create minted, inside that create's transaction — the component's
	// own machinery servicing itself, on a row nothing else can see until the
	// transaction commits.
	//
	// Everything else, without exception, names the scope.
	unscoped := []string{"GetListCreatedAt", "GetSignupCreatedAt"}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for statement := range strings.SplitSeq(Render(d), "-- name: ") {
				if statement == "" {
					continue
				}

				name := strings.Fields(statement)[0]
				if slices.Contains(unscoped, name) {
					continue
				}

				test.StrContains(t, statement, ScopeColumn, test.Sprintf("statement %q", name))
			}
		})
	}
}

// TestRender_SignupStatementsKeyOnTheirList is the property the signups table
// exists to have: every single-row statement over it names the list as well as
// the row's own id, so a caller holding one list's id cannot reach another
// list's signup.
func TestRender_SignupStatementsKeyOnTheirList(T *testing.T) {
	T.Parallel()

	keyed := []string{
		"GetSignup", "GetSignupByContactDigest",
		"UpdateSignupNotes", "TransitionSignup", "WithdrawSignup", "ArchiveSignup",
		"ListSignups", "ListSignupsDescending",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range keyed {
				statement := statementNamed(t, Render(d), name)

				test.StrContains(t, statement, SignupListColumn+" = ",
					test.Sprintf("%s does not key on the list", name))
			}
		})
	}
}

// TestRender_ContactDigestReadSeesArchivedRows pins the one read here rendered
// from a column list missing archived_at.
//
// The uniqueness on (scope, waitlist_id, contact_digest) covers archived and
// withdrawn rows, so a check that skipped them would report the contact free and
// hand the write to the index — a driver error where the caller wanted
// ErrContactWithdrawn, on the obligation this table carries that somebody else
// enforces on us.
func TestRender_ContactDigestReadSeesArchivedRows(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), "GetSignupByContactDigest")

			test.StrNotContains(t, statement, querygen.ArchivedAtColumn+" IS NULL")
			test.StrContains(t, statement, ScopeColumn+" = ")
			test.StrContains(t, statement, SignupContactDigestColumn+" = ")

			// It still projects the whole row, including archived_at, so the
			// store decides what an archived hit means rather than being unable
			// to see one.
			test.StrContains(t, statement, SignupsTable+"."+querygen.ArchivedAtColumn+"\n")

			// The read by id does exclude archived rows, because an archived
			// signup is not one a caller may act on.
			byID := statementNamed(t, Render(d), "GetSignup")
			test.StrContains(t, byID, querygen.ArchivedAtColumn+" IS NULL")
		})
	}
}

// TestRender_TransitionsAreGuarded is what makes a lifecycle move happen once.
//
// Both statements name the status column twice — in the SET that assigns it and
// in the predicate that requires the row to be in a particular state — and the
// two ends bind different arguments, so neither can set the column to the value
// it was requiring it to already hold. The withdrawal's guard is the inverted
// one, which is how a replay matches nothing.
func TestRender_TransitionsAreGuarded(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			transition := statementNamed(t, Render(d), "TransitionSignup")
			test.StrContains(t, transition, SignupStatusColumn+" = sqlc.arg("+SignupStatusColumn+")")
			test.StrContains(t, transition, SignupStatusColumn+" = sqlc.arg("+ExpectedStatusArg+")")

			withdraw := statementNamed(t, Render(d), "WithdrawSignup")
			test.StrContains(t, withdraw, SignupStatusColumn+" <> sqlc.arg("+ExpectedStatusArg+")")

			// The withdrawal blanks everything that identifies a person and
			// assigns the digest nothing, which is what lets the suppression
			// outlive the address.
			for _, column := range []string{
				SignupContactColumn, SignupNotesColumn, SignupSubjectTypeColumn, SignupSubjectIDColumn,
			} {
				test.StrContains(t, withdraw, column+" = sqlc.arg("+column+")")
			}

			test.StrNotContains(t, withdraw, SignupContactDigestColumn+" = sqlc.arg")

			// A note edit moves nobody: it assigns the note and the row's own
			// last_updated_at, and nothing else.
			notes := statementNamed(t, Render(d), "UpdateSignupNotes")
			test.StrNotContains(t, notes, SignupStatusChangedColumn+" =")
		})
	}
}

// TestRender_ErasureReachesEveryRowNamingTheSubject pins the three ways the
// erasure differs from the single-row withdrawal, each of which is a row an
// erasure would otherwise leave holding somebody's address.
func TestRender_ErasureReachesEveryRowNamingTheSubject(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			erasure := statementNamed(t, Render(d), "WithdrawSignupsForSubject")

			// It keys on the subject within the scope, not on a list and an id:
			// a person may be on several lists, and the one statement reaches
			// all of them. It is the one signup write that does not name the
			// list, which is why it is absent from the keyed set above.
			test.StrContains(t, erasure, ScopeColumn+" = ")
			test.StrContains(t, erasure, SignupSubjectTypeColumn+" = sqlc.arg("+ErasedSubjectTypeArg+")")
			test.StrContains(t, erasure, SignupSubjectIDColumn+" = sqlc.arg("+ErasedSubjectIDArg+")")
			test.StrNotContains(t, erasure, SignupListColumn+" = ")
			test.StrNotContains(t, erasure, "\t"+querygen.IDColumn+" = ")

			// It reaches archived rows. An archived signup still holds the
			// address it was made with, and an erasure that skipped it would
			// report completion over a row still naming somebody.
			test.StrNotContains(t, erasure, querygen.ArchivedAtColumn+" IS NULL")

			// It carries no status guard, because a withdrawn row has had its
			// subject blanked and cannot match a predicate naming one.
			test.StrNotContains(t, erasure, ExpectedStatusArg)

			// And it blanks exactly what the single-row withdrawal blanks, and
			// keeps exactly what it keeps: the two must not drift into two
			// meanings of "withdrawn".
			withdraw := statementNamed(t, Render(d), "WithdrawSignup")
			for _, column := range []string{
				SignupContactColumn, SignupNotesColumn, SignupSubjectTypeColumn, SignupSubjectIDColumn,
				SignupStatusColumn,
			} {
				test.StrContains(t, erasure, column+" = sqlc.arg("+column+")")
				test.StrContains(t, withdraw, column+" = sqlc.arg("+column+")")
			}

			test.StrContains(t, erasure, SignupStatusChangedColumn+" = sqlc.narg("+SignupStatusChangedColumn+")")
			test.StrNotContains(t, erasure, SignupContactDigestColumn+" = sqlc.arg")
		})
	}
}

// TestRender_OpenListReadBindsItsHorizon pins the comparison closes_at being NOT
// NULL buys: one bound instant, not a disjunction over the column the page walks
// by, and not the server's own clock.
func TestRender_OpenListReadBindsItsHorizon(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), "ListOpenLists")

			test.StrContains(t, statement, ListClosesAtColumn+" > sqlc.arg("+OpenAsOfArg+")")
			test.StrNotContains(t, statement, ListClosesAtColumn+" IS NULL")

			// The horizon is the caller's reading rather than CURRENT_TIMESTAMP,
			// because closes_at is stamped by the application — two clocks
			// deciding one row is what a test clock makes years apart.
			test.StrNotContains(t, statement, ListClosesAtColumn+" > CURRENT_TIMESTAMP")
		})
	}
}

// TestTable_KeyedColumns pins the idiom the keyed reads depend on: the column
// list a statement's predicates are derived from, without the id.
func TestTable_KeyedColumns(t *testing.T) {
	t.Parallel()

	for _, table := range allTables {
		keyed := table.KeyedColumns()

		test.False(t, slices.Contains(keyed, querygen.IDColumn), test.Sprintf("table %q", table.Name))
		test.EqOp(t, len(table.Columns)-1, len(keyed), test.Sprintf("table %q", table.Name))

		// Everything else survives, in order — the archived column above all,
		// since it is what keeps a keyed read from returning archived rows.
		test.True(t, slices.Contains(keyed, querygen.ArchivedAtColumn), test.Sprintf("table %q", table.Name))
	}
}

// TestTable_ColumnsExcept is what the one read that must see archived rows is
// rendered from.
func TestTable_ColumnsExcept(t *testing.T) {
	t.Parallel()

	trimmed := Signups.ColumnsExcept(querygen.IDColumn, querygen.ArchivedAtColumn)

	test.False(t, slices.Contains(trimmed, querygen.IDColumn))
	test.False(t, slices.Contains(trimmed, querygen.ArchivedAtColumn))
	test.EqOp(t, len(Signups.Columns)-2, len(trimmed))

	// Order is the projection's, which is what keeps a statement rendered from a
	// subset in step with the one rendered from the whole.
	test.True(t, slices.Index(trimmed, ScopeColumn) < slices.Index(trimmed, SignupStatusColumn))
}

// TestTables_ColumnsAreDeclaredOnce is the cheap check that catches the
// expensive mistake: a column list with a repeated or misspelled entry renders a
// statement that compiles and is wrong.
func TestTables_ColumnsAreDeclaredOnce(t *testing.T) {
	t.Parallel()

	for _, table := range allTables {
		seen := map[string]struct{}{}

		for _, column := range table.Columns {
			_, duplicate := seen[column]
			test.False(t, duplicate, test.Sprintf("table %q repeats %q", table.Name, column))
			seen[column] = struct{}{}
		}

		// Every table with rows of its own carries the convention triple and the
		// scope, which is what the standard statements are derived from.
		for _, column := range []string{
			querygen.IDColumn, ScopeColumn,
			querygen.CreatedAtColumn, querygen.LastUpdatedAtColumn, querygen.ArchivedAtColumn,
		} {
			test.True(t, slices.Contains(table.Columns, column),
				test.Sprintf("table %q has no %s", table.Name, column))
		}

		// Nullable and Updatable name real columns, since neither is checked by
		// anything downstream: querygen renders what it is handed.
		for _, column := range slices.Concat(table.Nullable, table.Updatable) {
			test.True(t, slices.Contains(table.Columns, column),
				test.Sprintf("table %q names %q, which is not one of its columns", table.Name, column))
		}
	}
}

// TestTable_UpdateColumns pins what the standard update may assign.
func TestTable_UpdateColumns(t *testing.T) {
	t.Parallel()

	// A list's update rewrites what it is called, what it is for, and when it
	// stops taking signups. Moving the closing time is how a list is extended
	// or brought in, which is why it is updatable at all.
	test.Eq(t, []string{ListNameColumn, ListDescriptionColumn, ListClosesAtColumn}, Lists.UpdateColumns())

	// The scope is immutable in both, so no write can move a row between
	// tenants, and neither create supplies its own creation time.
	for _, table := range allTables {
		test.False(t, slices.Contains(table.UpdateColumns(), ScopeColumn),
			test.Sprintf("table %q lets an update assign the scope", table.Name))
		test.False(t, slices.Contains(table.InsertColumns(), querygen.CreatedAtColumn),
			test.Sprintf("table %q lets a caller supply its creation time", table.Name))
	}

	// The signup's insert carries the digest, because a row without one is a
	// signup nothing can ever suppress.
	test.True(t, slices.Contains(Signups.InsertColumns(), SignupContactDigestColumn))
}

// statementNames reads the query names out of a rendered corpus, in order.
func statementNames(rendered string) []string {
	var names []string

	for line := range strings.SplitSeq(rendered, "\n") {
		if after, ok := strings.CutPrefix(line, "-- name: "); ok {
			names = append(names, strings.Fields(after)[0])
		}
	}

	return names
}

// statementNamed returns one statement out of a rendered corpus, failing the
// test when the corpus does not carry it.
func statementNamed(t *testing.T, rendered, name string) string {
	t.Helper()

	for statement := range strings.SplitSeq(rendered, "-- name: ") {
		if statement != "" && strings.Fields(statement)[0] == name {
			return statement
		}
	}

	t.Fatalf("the corpus has no statement named %q", name)

	return ""
}
