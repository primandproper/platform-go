package saga

import (
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// allDialects is the set every builder is rendered for, because the thing that
// breaks is placeholder numbering and only Postgres numbers.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// assertPlaceholders checks that the rendered query binds exactly as many
// values as it was given, which is the failure mode a builder actually has:
// a clause added without its argument, or an argument appended without its
// placeholder.
func assertPlaceholders(t *testing.T, d dialect.Dialect, query string, args []any) {
	t.Helper()

	if d == dialect.Postgres {
		for i := range args {
			test.StrContains(t, query, d.Placeholder(i+1),
				test.Sprintf("dialect %q query %q missing placeholder %d", d, query, i+1))
		}

		// One past the end must not appear, or an argument is unbound.
		test.StrNotContains(t, query, d.Placeholder(len(args)+1))

		return
	}

	test.EqOp(t, len(args), strings.Count(query, "?"),
		test.Sprintf("dialect %q query %q", d, query))
}

func TestTables(T *testing.T) {
	T.Parallel()

	T.Run("derives every name from one prefix", func(t *testing.T) {
		t.Parallel()

		tbl := newTables("app_saga")
		test.EqOp(t, "app_saga", tbl.prefix())
		test.EqOp(t, "app_saga_saga_instances", tbl.instances)
	})
}

func TestBuilders(T *testing.T) {
	T.Parallel()

	inst := newRecord("i1", "orders", []string{"one", "two"}, testState{Amount: 1}, baseTime)

	T.Run("insert binds every column it names", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := newTables("").buildInsertInstance(d, inst, []byte(`["one","two"]`), baseTime)

			test.StrContains(t, query, "INSERT INTO saga_instances")

			// One bound value per named column, or a column is being inserted
			// with somebody else's value.
			columns, _, found := strings.Cut(query, "VALUES")
			must.True(t, found)
			test.EqOp(t, strings.Count(columns, ",")+1, len(args))

			assertPlaceholders(t, d, query, args)
		}
	})

	T.Run("select renders the shared projection", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := newTables("").buildSelectInstance(d, "i1")

			test.StrContains(t, query, instanceColumns)
			assertPlaceholders(t, d, query, args)
		}
	})

	T.Run("list narrows and paginates", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			tbl := newTables("")

			query, args := tbl.buildListInstances(d, nil, "", 10, false)
			test.StrNotContains(t, query, "WHERE")
			test.StrContains(t, query, "ORDER BY id ASC")
			assertPlaceholders(t, d, query, args)

			query, args = tbl.buildListInstances(d, &ListScope{Definition: "orders"}, "cursor", 10, true)
			test.StrContains(t, query, "WHERE definition")
			test.StrContains(t, query, "ORDER BY id DESC")
			test.StrContains(t, query, "id < ")
			assertPlaceholders(t, d, query, args)

			query, args = tbl.buildListInstances(d, &ListScope{
				Statuses: []Status{StatusStuck, StatusCompensating},
			}, "", 10, false)
			test.StrContains(t, query, "status IN (")
			assertPlaceholders(t, d, query, args)

			query, args = tbl.buildCountInstances(d, &ListScope{Definition: "orders", Statuses: []Status{StatusStuck}})
			test.StrContains(t, query, "SELECT COUNT(*)")
			test.StrContains(t, query, " AND ")
			assertPlaceholders(t, d, query, args)

			query, args = tbl.buildCountInstances(d, &ListScope{})
			test.StrNotContains(t, query, "WHERE")
			assertPlaceholders(t, d, query, args)
		}
	})

	T.Run("claimable selects only advanceable, due, unleased rows", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := newTables("").buildSelectClaimable(d, baseTime, 5, true)

			test.StrContains(t, query, "status IN (")
			test.StrContains(t, query, "next_attempt <= ")
			test.StrContains(t, query, "claimed_until IS NULL OR claimed_until <= ")
			assertPlaceholders(t, d, query, args)

			// SKIP LOCKED only where the server has it.
			test.EqOp(t, d.SupportsSkipLocked(), strings.Contains(query, "FOR UPDATE SKIP LOCKED"))
		}
	})

	T.Run("claimable can be asked not to skip locked rows", func(t *testing.T) {
		t.Parallel()

		query, _ := newTables("").buildSelectClaimable(dialect.Postgres, baseTime, 5, false)
		test.StrNotContains(t, query, "SKIP LOCKED")
	})

	T.Run("claim increments attempts under a repeated status guard", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := newTables("").buildClaim(d, []string{"a", "b"}, baseTime.Add(time.Minute), baseTime)

			test.StrContains(t, query, "attempts = attempts + 1")
			test.StrContains(t, query, "status IN (")
			assertPlaceholders(t, d, query, args)
		}
	})

	T.Run("fetch by IDs reads back only advanceable rows", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := newTables("").buildFetchByIDs(d, []string{"a", "b", "c"})

			test.StrContains(t, query, instanceColumns)
			test.StrContains(t, query, "status IN (")
			assertPlaceholders(t, d, query, args)
		}
	})

	T.Run("advance keeps the lease mid-pass and drops it otherwise", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			tbl := newTables("")

			// Mid-pass: same instant, still running.
			query, args := tbl.buildAdvance(d, inst, baseTime, baseTime)
			test.StrNotContains(t, query, "claimed_until = NULL")
			test.StrContains(t, query, "attempts = 0")
			assertPlaceholders(t, d, query, args)

			// A delay was scheduled.
			query, args = tbl.buildAdvance(d, inst, baseTime.Add(time.Minute), baseTime)
			test.StrContains(t, query, "claimed_until = NULL")
			assertPlaceholders(t, d, query, args)

			// Terminal.
			done := *inst
			done.Status = StatusCompleted

			query, args = tbl.buildAdvance(d, &done, baseTime, baseTime)
			test.StrContains(t, query, "claimed_until = NULL")
			assertPlaceholders(t, d, query, args)
		}
	})

	T.Run("reschedule writes the attempt count and drops the lease", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := newTables("").buildReschedule(d, "i1", 3, baseTime, "boom", baseTime)

			test.StrContains(t, query, "claimed_until = NULL")
			test.StrContains(t, query, "status IN (")
			assertPlaceholders(t, d, query, args)
		}
	})

	T.Run("release touches nothing but the lease", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := newTables("").buildRelease(d, "i1", baseTime)

			test.StrContains(t, query, "claimed_until = NULL")
			test.StrNotContains(t, query, "next_attempt")
			test.StrNotContains(t, query, "status =")
			assertPlaceholders(t, d, query, args)
		}
	})

	T.Run("requeue clears the resume status and makes the row claimable", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, args := newTables("").buildRequeue(d, "i1", []Status{StatusStuck}, StatusCompensating, baseTime)

			test.StrContains(t, query, "resume_status = ''")
			test.StrContains(t, query, "attempts = 0")
			test.StrContains(t, query, "claimed_until = NULL")
			test.StrContains(t, query, "status IN (")
			assertPlaceholders(t, d, query, args)
		}
	})
}

func TestQueryHelpers(T *testing.T) {
	T.Parallel()

	T.Run("joinClause tolerates an empty predicate", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "a = 1", joinClause("", "a = 1"))
		test.EqOp(t, "a = 1 AND b = 2", joinClause("a = 1", "b = 2"))
	})

	T.Run("wherePrefix renders nothing for an empty predicate", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", wherePrefix(""))
		test.EqOp(t, " WHERE a = 1", wherePrefix("a = 1"))
	})
}
