package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/settings/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// allTables is every table declared here as a [Table], emitted or not.
var allTables = []*Table{&Definitions, &Values}

// TestRender_RegistersEveryTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// querygen.Generator.StandardCRUD registers what it emits for, which here is one
// table of three — so a consumer reading the registry back to truncate a
// database between integration tests would leave the options and the values
// full, and the symptom would be a different test failing later on rows the
// previous one left behind.
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
// halves of "what tables does settings own": the canonical spelling here, which
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
		"CreateDefinition", "GetDefinition", "ListDefinitions", "ListDefinitionsDescending",
		"UpdateDefinition", "ArchiveDefinition",
		"GetDefinitionCreatedAt",
		"GetDefinitionByName", "GetDefinitionIDByName",
		"DeleteDefinitionOptions", "InsertDefinitionOption", "ListDefinitionOptionsByDefinitionIDs",
		"GetValue",
		"ListValuesForSubject", "ListValuesForSubjectDescending",
		"ListValuesForDefinition", "ListValuesForDefinitionDescending",
		"UpsertValue", "ArchiveValue",
		"GetArchivedDefinition", "GetArchivedValue",
		"DeleteValuesForSubject",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			test.SliceEqFunc(t, want, statementNames(rendered), func(a, b string) bool { return a == b })

			// The values table gets no standard query: every one of them would
			// key on the id the table does not address rows by. Its create is
			// the upsert, and there is exactly one INSERT against it — and one
			// DELETE, which is the erasure and nothing else.
			test.StrNotContains(t, rendered, "CreateValue")
			test.StrNotContains(t, rendered, "UpdateValue")
			test.EqOp(t, 1, strings.Count(rendered, "INSERT INTO "+ValuesTable))
			test.EqOp(t, 1, strings.Count(rendered, "DELETE FROM "+ValuesTable))

			// Nothing here checks a row's existence without reading it.
			test.StrNotContains(t, rendered, "Existence")
		})
	}
}

// TestRender_ScopeIsInEveryStatement is the tenancy doctrine's third obligation,
// checked rather than remembered: no read path omits the scope.
func TestRender_ScopeIsInEveryStatement(T *testing.T) {
	T.Parallel()

	// The exceptions, in two groups, and neither is a read a caller reaches.
	//
	// The first is the read-back of the creation time a create's own INSERT just
	// caused, by the id that create minted, inside that create's transaction. It
	// is the component's own machinery servicing itself — the row is not visible
	// to anything else until the transaction commits — so it keys on the id
	// alone.
	//
	// The second is the option table's three statements, and their exception is
	// the schema's rather than the statements': an options row has no scope
	// column to name. It carries the id of the definition it hangs off and
	// nothing else, and that definition is the scoped row, so the writes bind an
	// id that came back from a scoped statement and the batched read keys on
	// ids read the same way. What keeps that safe is that no statement here
	// answers "which definitions admit this value", which is the one that would
	// need the column.
	//
	// Everything else, without exception, names the scope.
	unscoped := []string{
		"GetDefinitionCreatedAt",
		"DeleteDefinitionOptions", "InsertDefinitionOption", "ListDefinitionOptionsByDefinitionIDs",
	}

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

// TestRender_ValueStatementsKeyOnTheNaturalKey is the property the values table
// exists to have: a row is addressed by the scope, the subject and the
// definition, never by the id it carries for the cursor's sake.
func TestRender_ValueStatementsKeyOnTheNaturalKey(T *testing.T) {
	T.Parallel()

	keyed := []string{"GetValue", "ArchiveValue", "GetArchivedValue"}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range keyed {
				statement := statementNamed(t, Render(d), name)

				for _, column := range []string{
					ScopeColumn, ValueSubjectTypeColumn, ValueSubjectIDColumn, ValueDefinitionColumn,
				} {
					test.StrContains(t, statement, column+" = ", test.Sprintf("%s does not key on %s", name, column))
				}

				// The id is projected by the read and bound by neither.
				test.StrNotContains(t, statement, "sqlc.arg(id)")
			}

			// The upsert converges on the same four columns, because Postgres
			// matches ON CONFLICT against an index the table actually has and
			// the schema's is on exactly these.
			upsert := statementNamed(t, Render(d), "UpsertValue")
			test.StrContains(t, upsert, "archived_at = NULL")

			if d == dialect.Postgres || d == dialect.SQLite {
				test.StrContains(t, upsert, "ON CONFLICT (scope, subject_type, subject_id, definition_id)")
			} else {
				// MySQL names no conflict target at all — its ON DUPLICATE KEY
				// UPDATE fires on whichever unique key was violated.
				test.StrContains(t, upsert, "ON DUPLICATE KEY UPDATE")
			}
		})
	}
}

// TestRender_ErasureReachesClearedValues pins the shape the one delete over the
// values table has to have: keyed on the subject and the scope, and on nothing
// that would leave a row behind.
//
// A cleared value is an archived row that still says what the subject chose, so
// an erasure rendered with the archived predicate every other keyed statement
// carries would skip exactly the rows it exists to remove. And it keys on no
// definition, because an erasure is about a person rather than about a setting.
func TestRender_ErasureReachesClearedValues(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), "DeleteValuesForSubject")

			test.StrContains(t, statement, "DELETE FROM "+ValuesTable)

			for _, column := range []string{ScopeColumn, ValueSubjectTypeColumn, ValueSubjectIDColumn} {
				test.StrContains(t, statement, column+" = ", test.Sprintf("the erasure does not key on %s", column))
			}

			test.StrNotContains(t, statement, querygen.ArchivedAtColumn)
			test.StrNotContains(t, statement, ValueDefinitionColumn)
			test.StrNotContains(t, statement, "sqlc.arg(id)")
		})
	}
}

// TestRender_NameCollisionCheckSeesArchivedRows pins the read that has to see a
// name an archived definition still holds.
//
// The unique index covers archived definitions — archiving one does not destroy
// the values written under its name — so a check that skipped archived rows
// would report the name free and hand the write to the index, which is the
// driver error the read exists to prevent. querygen renders the archived
// predicate from the column list, so a read that must see archived rows is a
// read rendered from no columns.
func TestRender_NameCollisionCheckSeesArchivedRows(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), "GetDefinitionIDByName")

			test.StrNotContains(t, statement, querygen.ArchivedAtColumn)
			test.StrContains(t, statement, ScopeColumn)

			// The row being updated is excluded through an argument a caller may
			// leave unset, which is what lets one statement serve the create and
			// the rename both.
			test.StrContains(t, statement, "except_definition_id")

			// And the read every value-side call makes does exclude archived
			// rows, because a value cannot be written against a retired setting.
			byName := statementNamed(t, Render(d), "GetDefinitionByName")
			test.StrContains(t, byName, querygen.ArchivedAtColumn+" IS NULL")
		})
	}
}

// TestRender_ArchivedReadBacksSeeOnlyArchivedRows pins the pair of reads the two
// archives answer with.
//
// Every other single-row statement here filters archived_at IS NULL, which is
// what makes a retired setting absent from the catalog and a cleared answer
// absent from a resolution — so the read that describes what an archive just did
// is the one read that cannot make it. These carry the complement instead, which
// is the read-back asserting the thing it was called to confirm: a row the guard
// did not move is not a row either of them answers with.
func TestRender_ArchivedReadBacksSeeOnlyArchivedRows(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			definition := statementNamed(t, Render(d), "GetArchivedDefinition")

			test.StrContains(t, definition, querygen.ArchivedAtColumn+" IS NOT NULL")
			test.StrNotContains(t, definition, querygen.ArchivedAtColumn+" IS NULL")
			test.StrContains(t, definition, "sqlc.arg(id)")
			test.StrContains(t, definition, ScopeColumn+" = ")

			value := statementNamed(t, Render(d), "GetArchivedValue")

			test.StrContains(t, value, querygen.ArchivedAtColumn+" IS NOT NULL")
			test.StrNotContains(t, value, querygen.ArchivedAtColumn+" IS NULL")

			// Both project the whole row, because what a caller records about a
			// retirement or a clearing is the row rather than its key.
			for _, column := range Definitions.Columns {
				test.StrContains(t, definition, querygen.Qualify(DefinitionsTable, column))
			}

			for _, column := range Values.Columns {
				test.StrContains(t, value, querygen.Qualify(ValuesTable, column))
			}
		})
	}
}

// TestRender_OptionReadIsBatchedAndOrdered pins the shape the enumeration
// hydration depends on: one statement for a whole page of definitions, ordered
// so that one definition's options arrive together and in a stable order.
func TestRender_OptionReadIsBatchedAndOrdered(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), "ListDefinitionOptionsByDefinitionIDs")

			test.StrContains(t, statement, "ORDER BY")
			test.StrContains(t, statement, OptionValueColumn+" ASC")

			// The set is one bound array on Postgres and a placeholder expansion
			// on the other two — the same []string on either side of it.
			if d == dialect.Postgres {
				test.StrContains(t, statement, "= ANY(")
			} else {
				test.StrContains(t, statement, "IN (sqlc.slice(")
			}
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

// TestTable_UpdateColumns pins what a converging write may carry over.
func TestTable_UpdateColumns(t *testing.T) {
	t.Parallel()

	// The definition's update assigns every column that is not the id, the
	// scope, or one the database owns — which is unusual in this module and is
	// what the stranded-value walk in the store is the guard for.
	test.Eq(t, []string{"name", "description", "kind", "default_value", "admin_only"},
		Definitions.UpdateColumns())

	// A value's converging write assigns the answer and nothing about whose
	// answer it is.
	test.Eq(t, []string{ValueColumn}, Values.UpdateColumns())

	// The scope is immutable in both, so no write can move a row between
	// tenants.
	for _, table := range allTables {
		test.False(t, slices.Contains(table.UpdateColumns(), ScopeColumn),
			test.Sprintf("table %q lets an update assign the scope", table.Name))
		test.False(t, slices.Contains(table.InsertColumns(), querygen.CreatedAtColumn),
			test.Sprintf("table %q lets a caller supply its creation time", table.Name))
	}
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
