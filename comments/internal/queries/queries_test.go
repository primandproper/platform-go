package queries

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/comments/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// statementName finds each statement's sqlc annotation.
var statementName = regexp.MustCompile(`(?m)^-- name: (\S+) `)

// TestRender_RegistersEveryTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// This table takes no querygen.Generator.StandardCRUD — which is what registers
// a table elsewhere in this module — so without the explicit registration a
// consumer reading the registry back to truncate a database between integration
// tests would not find it, and the symptom would be a different test failing
// later on rows the previous one left behind.
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
// halves of "what tables does comments own": the canonical spelling here, which
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
		"CreateComment", "UpdateComment", "ArchiveComment",
		"DeleteCommentsForTarget", "DeleteCommentsByAuthor",
		"GetComment", "GetCommentCreatedAt",
		"ListComments", "ListCommentsDescending",
		"ListCommentsByTargetType", "ListCommentsByTargetTypeDescending",
		"ListCommentsByAuthor", "ListCommentsByAuthorDescending",
	}

	slices.Sort(want)

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			got := statementNames(Render(d))
			slices.Sort(got)

			test.Eq(t, want, got)
		})
	}
}

// TestRender_ScopesEveryStatement is the tenancy doctrine asserted over the text
// rather than over the store.
//
// There is no exception in this corpus, and that is the assertion: every
// statement names the scope, and the one that keys on nothing — the insert —
// names it as a column it stores. A statement added without one would be a read
// that answers across tenants, and nothing about its result would say so.
func TestRender_ScopesEveryStatement(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, body := range statements(Render(d)) {
				test.StrContains(t, body, ScopeColumn,
					test.Sprintf("%s does not name the scope", name))

				if strings.Contains(body, "WHERE") {
					test.StrContains(t, body, ScopeColumn+" =",
						test.Sprintf("%s does not filter on the scope", name))
				}
			}
		})
	}
}

// TestRender_TheDiscussionIsOneStatement is the reading the empty parent bought.
//
// The roots of a target and one root's replies are the same text with a
// different bound value, which is only true because parent_id holds the empty
// string rather than NULL. If the column ever went nullable, the roots would
// need `IS NULL` — statement text — and this list would have to become two.
func TestRender_TheDiscussionIsOneStatement(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			body := statements(Render(d))["ListComments"]
			must.NotEq(t, "", body)

			test.StrContains(t, body, ParentIDColumn+" =",
				test.Sprint("the discussion read must bind the parent rather than test it for NULL"))
			test.StrContains(t, body, TargetTypeColumn+" =")
			test.StrContains(t, body, TargetIDColumn+" =")
		})
	}
}

// TestRender_TheEditAssignsTheBodyAlone is the other half of what an edit means.
//
// A comment's target is what it is about and was checked against the consumer's
// catalog when it was written; its parent is which conversation it is in; its
// author is who said it. An edit that assigned any of the three would move
// somebody else's words, with nothing anywhere reporting it.
func TestRender_TheEditAssignsTheBodyAlone(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			body := statements(Render(d))["UpdateComment"]
			must.NotEq(t, "", body)

			set, _, found := strings.Cut(body, "WHERE")
			must.True(t, found, must.Sprint("the edit must key on something"))

			test.StrContains(t, set, BodyColumn+" = ")

			for _, column := range []string{TargetTypeColumn, TargetIDColumn, ParentIDColumn, AuthorColumn} {
				test.StrNotContains(t, set, column,
					test.Sprintf("the edit assigns %s, which is not the author's to change", column))
			}
		})
	}
}

// TestRender_TheTwoDeletesReachArchivedRows is the pair of statements whose
// absence of a predicate is the point.
//
// A subject access request runs against whatever the subject left behind, and a
// comment they wrote that a moderator archived is still a sentence they wrote. A
// sweep is the same case from the other side: a target that is going away takes
// every comment about it, and one somebody had already archived is still a row
// about a thing that no longer exists.
func TestRender_TheTwoDeletesReachArchivedRows(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := statements(Render(d))

			for _, name := range []string{"DeleteCommentsByAuthor", "DeleteCommentsForTarget"} {
				body := rendered[name]
				must.NotEq(t, "", body, must.Sprintf("%s is not emitted", name))

				test.StrContains(t, body, "DELETE FROM")
				test.StrNotContains(t, body, querygen.ArchivedAtColumn,
					test.Sprintf("%s skips archived rows, which is exactly what it must not leave behind", name))
			}
		})
	}
}

func TestTable_ColumnsExcept(t *testing.T) {
	t.Parallel()

	// Leaving the id out is how a statement says it keys on something else, so
	// the order of what remains has to be the projection's — the generated
	// params struct follows it.
	test.Eq(t, Comments.Columns[1:], Comments.ColumnsExcept(querygen.IDColumn))
	test.SliceNotContains(t, Comments.ColumnsExcept(querygen.IDColumn), querygen.IDColumn)

	test.Eq(t, Comments.Columns, Comments.ColumnsExcept())
}

func TestTable_InsertColumns(t *testing.T) {
	t.Parallel()

	// created_at is the database's, which is why the schema gives it a DEFAULT.
	test.SliceNotContains(t, Comments.InsertColumns(), querygen.CreatedAtColumn)
	test.SliceNotContains(t, Comments.InsertColumns(), querygen.LastUpdatedAtColumn)
	test.SliceNotContains(t, Comments.InsertColumns(), querygen.ArchivedAtColumn)
	test.SliceContains(t, Comments.InsertColumns(), ParentIDColumn)
	test.SliceContains(t, Comments.InsertColumns(), TargetTypeColumn)
	test.SliceContains(t, Comments.InsertColumns(), TargetIDColumn)
}

func TestFileName(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "postgres_generated.sql", FileName(dialect.Postgres))
}

// statements splits a rendered corpus into its named statements.
func statements(rendered string) map[string]string {
	out := map[string]string{}

	matches := statementName.FindAllStringSubmatchIndex(rendered, -1)
	for i, m := range matches {
		end := len(rendered)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}

		out[rendered[m[2]:m[3]]] = rendered[m[0]:end]
	}

	return out
}

// statementNames is the names alone, for the set assertion.
func statementNames(rendered string) []string {
	names := make([]string, 0, len(statements(rendered)))
	for name := range statements(rendered) {
		names = append(names, name)
	}

	return names
}
