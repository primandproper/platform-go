package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/shredding/migrations"

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
				test.Sprintf("run `make unison` and commit %s", FileName(d)))
		})
	}
}

// TestRender_RegistersTheTable is the registry half of the same guarantee the
// canonical .sql files are the query half of: a consumer reading the registry
// back to truncate a database between integration tests gets every table this
// component has rows in.
func TestRender_RegistersTheTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	for _, table := range TableNames {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

// TestColumns_AreTheColumnsTheDDLDeclares is the cross-check between the two
// halves of "what does this table hold": the list here, which every statement
// is rendered from, and the DDL a consumer actually creates the table with.
//
// Neither derives from the other on purpose, so a column added to one and not
// the other stops being invisible. It matters most for the two columns
// [RecordColumns] leaves out: archived_at buys the statements no predicate and
// last_updated_at buys them no stamp, and both of those are decisions about a
// column that has to exist.
func TestColumns_AreTheColumnsTheDDLDeclares(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			must.StrContains(t, ddl, SubjectKeysTable)

			for _, column := range Columns {
				test.StrContains(t, ddl, column, test.Sprintf("%s is not in the %s DDL", column, d))
			}
		})
	}
}

// TestRender_EmitsTheStatementsTheStoreExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
//
// Three, for four call sites. The mint and the tombstone share InsertSubjectKey
// because they are the same write — see the package comment.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	want := []string{"GetSubjectKey", "InsertSubjectKey", "ShredSubjectKey"}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.Eq(t, want, statementNames(Render(d)))
		})
	}
}

// TestRender_KeysEveryStatementOnTheNaturalKey is the one property this schema
// cannot be wrong about.
//
// (subject_type, subject_id) is what enforces one live key per subject, and one
// live key per subject is the difference between a shred that works and one
// that leaves half the ciphertext readable. A statement that addressed a row by
// the subject id alone would silently merge a user and an account that happen
// to share an identifier.
func TestRender_KeysEveryStatementOnTheNaturalKey(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			// The read and the shred address the pair in their predicates; the
			// insert names it as the conflict target on the one dialect that
			// spells a target at all.
			test.EqOp(t, 2, strings.Count(rendered, "subject_type = sqlc.arg(subject_type)"))
			test.EqOp(t, 2, strings.Count(rendered, "subject_id = sqlc.arg(subject_id)"))
		})
	}
}

// TestRender_SkipsADuplicateRowInEachDialectsSpelling pins the clause that is
// load-bearing rather than cosmetic: it is how the loser of a mint race learns
// to discard its key instead of writing a second live one, and how a mint after
// a tombstone is refused.
//
// It is spelled three different ways, and only Postgres names the key it is
// skipping a collision on — which is the one that has to match the table's
// unique index exactly, or the server rejects the statement at analysis rather
// than at the first collision.
func TestRender_SkipsADuplicateRowInEachDialectsSpelling(T *testing.T) {
	T.Parallel()

	T.Run("postgres names the natural key as the conflict target", func(t *testing.T) {
		t.Parallel()

		rendered := Render(dialect.Postgres)

		test.StrContains(t, rendered, "ON CONFLICT (subject_type, subject_id) DO NOTHING")
		test.StrNotContains(t, rendered, "INSERT IGNORE")
	})

	T.Run("mysql and sqlite take a modifier and name no target", func(t *testing.T) {
		t.Parallel()

		mysql := Render(dialect.MySQL)
		test.StrContains(t, mysql, "INSERT IGNORE INTO")
		test.StrNotContains(t, mysql, "ON CONFLICT")

		sqlite := Render(dialect.SQLite)
		test.StrContains(t, sqlite, "INSERT OR IGNORE INTO")
		test.StrNotContains(t, sqlite, "ON CONFLICT")
	})
}

// TestRender_GuardsTheShredOnTheTombstone pins the guard that makes the
// destruction idempotent without a read first: a second call matches nothing,
// and zero rows affected is how the caller learns the destruction was somebody
// else's.
//
// It also pins that the guard binds nothing. A guard a caller could pass a
// value for is a guard a caller could disarm.
func TestRender_GuardsTheShredOnTheTombstone(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			shred := statement(t, Render(d), "ShredSubjectKey")

			test.StrContains(t, shred, "AND shredded_at IS NULL")
			test.StrNotContains(t, shred, "shredded_at = sqlc.arg")
		})
	}
}

// TestRender_StampsTheShredFromOneInstant pins the one place this schema asks
// for something the conventional tables do not.
//
// This is the only statement that rewrites a key row, so last_updated_at and
// shredded_at describe one event. querygen stamps last_updated_at from the
// server's clock for any statement whose shape list names the column, and two
// clock reads could have the two columns disagree about when the destruction
// happened — so the shape list leaves it out and the SET list binds it.
func TestRender_StampsTheShredFromOneInstant(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			shred := statement(t, Render(d), "ShredSubjectKey")

			test.StrContains(t, shred, "last_updated_at = sqlc.narg(last_updated_at)")
			test.StrNotContains(t, shred, "last_updated_at = CURRENT_TIMESTAMP")
		})
	}
}

// TestRender_ReadsAndWritesArchivedRowsAlike pins the absence of the predicate
// every conventional single-row statement carries.
//
// archived_at is in the schema for the convention's sake and nothing writes it.
// A read that excluded archived rows would report a shredded subject as one
// that never had a key, and the tombstone is the evidence a destruction
// happened — so no statement here filters on the column.
func TestRender_ReadsAndWritesArchivedRowsAlike(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.StrNotContains(t, Render(d), "archived_at")
		})
	}
}

// TestRecordColumns_AreTheRowMinusTheTwoNothingSupplies pins the gap between
// the whole row and the list every statement is rendered from, since that gap
// is what leaves the statements with no archived predicate and no stamp of
// their own.
func TestRecordColumns_AreTheRowMinusTheTwoNothingSupplies(t *testing.T) {
	t.Parallel()

	for _, column := range RecordColumns {
		test.True(t, slices.Contains(Columns, column),
			test.Sprintf("%s is projected but is not a column", column))
	}

	test.False(t, slices.Contains(RecordColumns, querygen.ArchivedAtColumn))
	test.False(t, slices.Contains(RecordColumns, querygen.LastUpdatedAtColumn))
}

// TestFileName names the file each dialect is committed to.
func TestFileName(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "postgres_generated.sql", FileName(dialect.Postgres))
	test.EqOp(t, "mysql_generated.sql", FileName(dialect.MySQL))
	test.EqOp(t, "sqlite_generated.sql", FileName(dialect.SQLite))
}

// statementNames reads the query names out of a rendered file, in order.
func statementNames(rendered string) []string {
	var names []string

	for line := range strings.SplitSeq(rendered, "\n") {
		if after, found := strings.CutPrefix(line, "-- name: "); found {
			names = append(names, strings.Fields(after)[0])
		}
	}

	return names
}

// statement returns one named statement's text out of a rendered file, so an
// assertion about the shred is not accidentally satisfied by the read.
func statement(t *testing.T, rendered, name string) string {
	t.Helper()

	for block := range strings.SplitSeq(rendered, "-- name: ") {
		if strings.HasPrefix(block, name+" ") {
			return block
		}
	}

	t.Fatalf("no statement named %q in the rendered file", name)

	return ""
}
