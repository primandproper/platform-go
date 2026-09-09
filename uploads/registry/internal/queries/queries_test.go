package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/uploads/registry/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// TestRender_RegistersEveryTable is the registry half of the same guarantee the
// canonical .sql files are the query half of: a consumer reading the registry
// back to truncate a database between integration tests gets every table, not
// only the ones something currently emits SQL for.
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
// halves of "what tables does this package own": the canonical spelling here,
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

// TestObjectColumns_AreTheColumnsTheDDLDeclares pins the other half of the same
// pairing, at the column level: the projection order every read is written in
// against the table that has to carry it.
//
// A column list that named something the schema does not have would fail at
// `sqlc compile`; one that omitted a column the schema does have would not fail
// anywhere. The row would simply never be projected, and the field on the
// domain type would be permanently zero.
func TestObjectColumns_AreTheColumnsTheDDLDeclares(t *testing.T) {
	t.Parallel()

	stmts, err := migrations.Statements(dialect.Postgres, "")
	must.NoError(t, err)

	var declared []string

	for _, stmt := range stmts {
		if !strings.HasPrefix(stmt, "CREATE TABLE") {
			continue
		}

		for line := range strings.SplitSeq(stmt, "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) < 2 || strings.HasPrefix(fields[0], "--") || strings.HasPrefix(fields[0], "CREATE") {
				continue
			}

			if name := strings.TrimSuffix(fields[0], ","); slices.Contains(ObjectColumns, name) {
				declared = append(declared, name)
			}
		}
	}

	test.Eq(t, ObjectColumns, declared)
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
				test.Sprintf("run `go generate ./uploads/registry/...` and commit %s", FileName(d)))
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
		"CreateObject", "GetObject", "ListObjects", "ListObjectsDescending", "ArchiveObject",
		"ListObjectsByOwner", "ListObjectsByOwnerDescending",
		"ListObjectsBySubject", "ListObjectsBySubjectDescending",
		"GetObjectByKey", "GetObjectIDByKey", "GetObjectCreatedAt",
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

			// The two omissions, and both are the shape of the table rather
			// than a gap. Nothing edits a row — every column is a fact about
			// bytes already in a bucket — and nothing asks whether a row exists
			// without reading it, because whether a caller may see an object is
			// decided from the row they would have read anyway.
			test.StrNotContains(t, rendered, "UpdateObject")
			test.StrNotContains(t, rendered, "Existence")
		})
	}
}

// TestRender_ScopeIsInEveryStatement is the tenancy obligation read off the
// emitted text: no statement omits the scope, so there is no read a caller can
// reach that answers across tenants.
//
// It is asserted over the rendered SQL rather than over the call sites that
// build it, because the call site is where the predicate is asked for and the
// text is where it either is or is not.
func TestRender_ScopeIsInEveryStatement(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for statement := range strings.SplitSeq(Render(d), "-- name: ") {
				name, body, ok := strings.Cut(statement, "\n")
				if !ok {
					continue
				}

				test.StrContains(t, body, ScopeColumn,
					test.Sprintf("%s omits the scope", strings.Fields(name)[0]))
			}
		})
	}
}

// TestRender_UniquenessCheckReadsArchivedRows pins the one read here that
// deliberately carries no archived predicate.
//
// The unique index on (scope, object_key) covers archived rows, because
// archival is metadata-only and the bytes stay in the bucket. A collision check
// that skipped archived rows would report a key free and then watch the index
// refuse the insert — the error the check exists to turn into a sentinel.
func TestRender_UniquenessCheckReadsArchivedRows(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statements := map[string]string{}
			for statement := range strings.SplitSeq(Render(d), "-- name: ") {
				if name, body, ok := strings.Cut(statement, "\n"); ok {
					statements[strings.Fields(name)[0]] = body
				}
			}

			check := statements["GetObjectIDByKey"]
			must.StrContains(t, check, ObjectKeyColumn)
			test.StrNotContains(t, check, "archived_at")

			// The read a request runs is the other way round: an archived row
			// is a row nobody may fetch through.
			read := statements["GetObjectByKey"]
			test.StrContains(t, read, "archived_at IS NULL")
		})
	}
}

// TestRender_InsertLeavesTheConventionTimestampsToTheDatabase pins that the
// create supplies no timestamp of its own.
//
// A caller-supplied creation time is how a row ends up with one that disagrees
// with its id, and the cursor walk orders by id while the filter window compares
// created_at.
func TestRender_InsertLeavesTheConventionTimestampsToTheDatabase(t *testing.T) {
	t.Parallel()

	inserted := InsertColumns()

	for _, column := range []string{querygen.CreatedAtColumn, querygen.LastUpdatedAtColumn, querygen.ArchivedAtColumn} {
		test.False(t, slices.Contains(inserted, column),
			test.Sprintf("the create supplies %s", column))
	}

	// Everything else the table has, though: a column added to the list and
	// left out of the insert is a column no write can ever fill.
	test.SliceLen(t, len(ObjectColumns)-3, inserted)
}
