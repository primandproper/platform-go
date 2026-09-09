package queries_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/saga/internal/queries"
	"github.com/primandproper/platform-go/v14/saga/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// dialects is every dialect this package renders for. A statement that only one
// server would parse is a statement whose consumer cannot switch databases, and
// the whole corpus is checked against all three by .scripts/sqlc_compile.sh —
// so what these tests check is the text, not the parse.
var dialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

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

func TestRender(T *testing.T) {
	T.Parallel()

	T.Run("every dialect renders the same statements", func(t *testing.T) {
		t.Parallel()

		want := statementNames(queries.Render(dialect.Postgres))
		must.SliceNotEmpty(t, want)

		for _, d := range dialects {
			test.Eq(t, want, statementNames(queries.Render(d)), test.Sprintf("dialect %q", d))
		}
	})

	T.Run("every statement the store calls is rendered", func(t *testing.T) {
		t.Parallel()

		// The names the store spells, which are Go method names on the
		// generated querier. A corpus missing one of these compiles, checks
		// clean, and fails to build the package that calls it — this says so
		// here instead.
		expected := []string{
			queries.GetInstanceQuery,
			queries.ListInstancesQuery,
			querygen.DescendingName(queries.ListInstancesQuery),
			queries.ListByDefinitionQuery,
			querygen.DescendingName(queries.ListByDefinitionQuery),
			queries.ClaimableIDsQuery,
			queries.ListByIDsQuery,
			queries.InsertInstanceQuery,
			queries.ClaimInstancesQuery,
			queries.AdvanceQuery,
			queries.AdvanceAndClearLeaseQuery,
			queries.RescheduleQuery,
			queries.ReleaseQuery,
			queries.RequeueQuery,
		}

		rendered := statementNames(queries.Render(dialect.Postgres))

		test.Eq(t, expected, rendered)
	})

	T.Run("a paged read is rendered in both directions", func(t *testing.T) {
		t.Parallel()

		for _, d := range dialects {
			rendered := queries.Render(d)

			for _, name := range []string{queries.ListInstancesQuery, queries.ListByDefinitionQuery} {
				ascending := "-- name: " + name + " :many"
				descending := "-- name: " + querygen.DescendingName(name) + " :many"

				test.StrContains(t, rendered, ascending, test.Sprintf("dialect %q", d))
				test.StrContains(t, rendered, descending, test.Sprintf("dialect %q", d))

				// The pair differs in its ORDER BY and its cursor comparison
				// and in nothing else, which is what lets the store convert one
				// page type to the other rather than restating it.
				test.StrContains(t, rendered, "ORDER BY saga_instances.id DESC")
			}
		}
	})

	T.Run("the corpus renders no SQL the store composes", func(t *testing.T) {
		t.Parallel()

		// The prefix marker is substituted by the generated package at
		// construction; nothing else in a rendered statement is interpolated,
		// and a stray Go format verb here would be a value nobody bound.
		for _, d := range dialects {
			test.False(t, strings.Contains(queries.Render(d), "%!"), test.Sprintf("dialect %q", d))
			test.False(t, strings.Contains(queries.Render(d), "{{"), test.Sprintf("dialect %q", d))
		}
	})

	T.Run("every rendered statement is terminated and annotated", func(t *testing.T) {
		t.Parallel()

		for _, d := range dialects {
			for statement := range strings.SplitSeq(strings.TrimSpace(queries.Render(d)), "\n\n") {
				test.True(t, strings.HasPrefix(statement, "-- name: "), test.Sprintf("dialect %q: %s", d, statement))
				test.True(t, strings.HasSuffix(statement, ";"), test.Sprintf("dialect %q: %s", d, statement))
			}
		}
	})
}

func TestRender_ClaimShape(T *testing.T) {
	T.Parallel()

	T.Run("the row lock is taken where the server has one", func(t *testing.T) {
		t.Parallel()

		// SQLite has neither the clause nor the concurrency it exists for. The
		// other two take it, and without it the candidate rows are unlocked
		// before the lease is written and two workers claim the same batch.
		for _, d := range dialects {
			test.EqOp(t, d.SupportsSkipLocked(),
				strings.Contains(queries.Render(d), "FOR UPDATE SKIP LOCKED"),
				test.Sprintf("dialect %q", d))
		}
	})

	T.Run("the claim's two reads agree about the order", func(t *testing.T) {
		t.Parallel()

		// A worker that selected in one order and read back in another would
		// hand its steps out in an order nothing chose.
		const order = "ORDER BY saga_instances.next_attempt, saga_instances.created_at, saga_instances.id"

		for _, d := range dialects {
			test.EqOp(t, 2, strings.Count(queries.Render(d), order), test.Sprintf("dialect %q", d))
		}
	})

	T.Run("a bound set is the last argument in the statement that binds one", func(t *testing.T) {
		t.Parallel()

		// SQLite numbers a bare marker one past the highest it has seen, so an
		// argument bound after an expanded set collides with the set's own
		// elements. The rule is the statement's shape rather than the store's
		// care, so it is checked here.
		rendered := queries.Render(dialect.SQLite)

		for statement := range strings.SplitSeq(strings.TrimSpace(rendered), "\n\n") {
			marker := strings.Index(statement, "sqlc.slice(")
			if marker < 0 {
				continue
			}

			test.False(t, strings.Contains(statement[marker:], "sqlc.arg("), test.Sprintf("%s", statement))
			test.False(t, strings.Contains(statement[marker:], "sqlc.narg("), test.Sprintf("%s", statement))
		}
	})
}

func TestColumns(T *testing.T) {
	T.Parallel()

	T.Run("the create supplies everything but what a lease and an update own", func(t *testing.T) {
		t.Parallel()

		test.SliceNotContains(t, queries.InsertColumns, querygen.LastUpdatedAtColumn)
		test.SliceNotContains(t, queries.InsertColumns, querygen.ArchivedAtColumn)
		test.SliceNotContains(t, queries.InsertColumns, queries.ClaimedUntilColumn)

		// created_at is supplied, which is this schema's one departure from the
		// module's convention — see the package comment.
		test.SliceContains(t, queries.InsertColumns, querygen.CreatedAtColumn)

		// Order is the projection's, because an INSERT's column list and its
		// VALUES list are read together.
		test.True(t, slices.IsSortedFunc(queries.InsertColumns, func(a, b string) int {
			return slices.Index(queries.Columns, a) - slices.Index(queries.Columns, b)
		}))
	})

	T.Run("the canonical table name is the one the DDL creates", func(t *testing.T) {
		t.Parallel()

		// Two spellings of one table is the drift this package exists to
		// prevent, and neither half is derived from the other: this list is
		// written down here and migrations.Tables reads the schema.
		tables, err := migrations.Tables("")
		must.NoError(t, err)

		test.Eq(t, queries.TableNames, tables)
	})

	T.Run("the table is registered", func(t *testing.T) {
		t.Parallel()

		queries.Render(dialect.Postgres)

		for _, table := range queries.TableNames {
			test.True(t, querygen.TableRegistered(table), test.Sprintf("table %q", table))
		}
	})
}
