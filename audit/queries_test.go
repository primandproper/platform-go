package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v10/database/dialect"
	"github.com/primandproper/platform-go/v10/filtering"
	"github.com/primandproper/platform-go/v10/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The SQL these render is exercised end to end by the SQLite tests and the
// container suite. It is also asserted here, directly, because the parts that
// differ between dialects are exactly the parts a single-dialect test cannot
// see: whether Postgres got its numbered placeholders, whether the row lock is
// present where it is needed and absent where it is unsupported, and whether
// each dialect's spelling of "skip a row that already exists" is the one that
// dialect actually accepts.

var testTables = newTables(DefaultTablePrefix)

// allDialects is every dialect this package emits SQL for.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

func TestBuildSelectChainHead(T *testing.T) {
	T.Parallel()

	T.Run("locks the row where the dialect can", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL} {
			query, args := testTables.buildSelectChainHead(d, "acct_1", true)

			// The lock is what serializes concurrent writers into one scope;
			// losing it turns a wait into a unique-constraint failure in
			// somebody's business transaction.
			test.StrHasSuffix(t, " FOR UPDATE", query, test.Sprintf("dialect %q", d))
			test.Eq(t, []any{"acct_1"}, args)
		}
	})

	T.Run("omits the lock on SQLite, which has none and needs none", func(t *testing.T) {
		t.Parallel()

		query, _ := testTables.buildSelectChainHead(dialect.SQLite, "acct_1", true)
		test.StrNotContains(t, query, "FOR UPDATE")
	})

	T.Run("omits the lock when not asked for one", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, _ := testTables.buildSelectChainHead(d, "acct_1", false)
			test.StrNotContains(t, query, "FOR UPDATE", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("numbers placeholders for Postgres only", func(t *testing.T) {
		t.Parallel()

		pg, _ := testTables.buildSelectChainHead(dialect.Postgres, "acct_1", false)
		test.StrContains(t, pg, "scope = $1")

		my, _ := testTables.buildSelectChainHead(dialect.MySQL, "acct_1", false)
		test.StrContains(t, my, "scope = ?")
	})
}

func TestBuildInsertChain(T *testing.T) {
	T.Parallel()

	// Each dialect spells "leave the existing row alone" differently, and
	// getting it wrong means the first two writers to a brand new scope race,
	// with the loser's business transaction failing on a primary key.
	T.Run("skips an existing row in each dialect's own spelling", func(t *testing.T) {
		t.Parallel()

		now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)

		pg, args := testTables.buildInsertChain(dialect.Postgres, "acct_1", now)
		test.StrContains(t, pg, "ON CONFLICT (scope) DO NOTHING")
		test.StrHasPrefix(t, "INSERT INTO", pg)
		test.Eq(t, []any{"acct_1", now}, args)

		my, _ := testTables.buildInsertChain(dialect.MySQL, "acct_1", now)
		test.StrHasPrefix(t, "INSERT IGNORE INTO", my)
		test.StrNotContains(t, my, "ON CONFLICT")

		lite, _ := testTables.buildInsertChain(dialect.SQLite, "acct_1", now)
		test.StrHasPrefix(t, "INSERT OR IGNORE INTO", lite)
		test.StrNotContains(t, lite, "ON CONFLICT")
	})

	T.Run("seeds a chain that has recorded and pruned nothing", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			query, _ := testTables.buildInsertChain(d, "acct_1", time.Time{})

			// -1 rather than 0, so the first entry recorded lands at position
			// zero after the pre-increment.
			test.StrContains(t, query, "-1, '', -1, ''", test.Sprintf("dialect %q", d))
		}
	})
}

func TestIgnorePrefix(T *testing.T) {
	T.Parallel()

	T.Run("renders nothing for an unknown dialect", func(t *testing.T) {
		t.Parallel()

		// Unreachable through the constructors, which reject an invalid dialect
		// before any query is built. Asserted anyway so the fallback is a plain
		// INSERT rather than something the server would reject outright.
		test.EqOp(t, "", ignorePrefix("cassandra"))
	})
}

func TestBuildInsertEntries(T *testing.T) {
	T.Parallel()

	T.Run("binds every column of every row", func(t *testing.T) {
		t.Parallel()

		rows := []entryRow{
			{id: "a", seq: 0, scope: "acct_1", hash: "h0"},
			{id: "b", seq: 1, scope: "acct_1", hash: "h1"},
		}

		query, args := testTables.buildInsertEntries(dialect.Postgres, rows)

		test.SliceLen(t, len(rows)*columnsPerRow, args)
		test.StrContains(t, query, entryColumns)
		test.StrContains(t, query, "$14")
		test.StrContains(t, query, "$28")
	})

	T.Run("stays inside SQLite's parameter ceiling at a full batch", func(t *testing.T) {
		t.Parallel()

		// 999 is the ceiling on SQLite builds before 3.32, and it is what
		// maxBatchRows is derived from.
		test.Less(t, 999, maxBatchRows*columnsPerRow)
	})
}

func TestBuildSelectChainRange(T *testing.T) {
	T.Parallel()

	T.Run("orders by position, not by time", func(t *testing.T) {
		t.Parallel()

		query, _ := testTables.buildSelectChainRange(dialect.SQLite, "acct_1", time.Time{}, time.Time{})

		// Two entries recorded in one transaction share a timestamp, so ordering
		// by time would sometimes hand the walk a pair in the wrong order and
		// report an intact chain as broken.
		test.StrHasSuffix(t, "ORDER BY seq", query)
	})

	T.Run("leaves a zero bound unbounded", func(t *testing.T) {
		t.Parallel()

		query, args := testTables.buildSelectChainRange(dialect.SQLite, "acct_1", time.Time{}, time.Time{})

		// recorded_at is in the projection either way; what must be absent is a
		// predicate over it.
		test.StrNotContains(t, query, "recorded_at >=")
		test.StrNotContains(t, query, "recorded_at <=")
		test.SliceLen(t, 1, args)
	})

	T.Run("applies each bound it is given", func(t *testing.T) {
		t.Parallel()

		from := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
		to := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)

		query, args := testTables.buildSelectChainRange(dialect.Postgres, "acct_1", from, time.Time{})
		test.StrContains(t, query, "recorded_at >= $2")
		test.SliceLen(t, 2, args)

		query, args = testTables.buildSelectChainRange(dialect.Postgres, "acct_1", time.Time{}, to)
		test.StrContains(t, query, "recorded_at <= $2")
		test.SliceLen(t, 2, args)

		query, args = testTables.buildSelectChainRange(dialect.Postgres, "acct_1", from, to)
		test.StrContains(t, query, "recorded_at >= $2")
		test.StrContains(t, query, "recorded_at <= $3")
		test.SliceLen(t, 3, args)
	})
}

func TestBuildListEntries(T *testing.T) {
	T.Parallel()

	T.Run("pages forward by default", func(t *testing.T) {
		t.Parallel()

		filter := filtering.DefaultQueryFilter()
		filter.Cursor = pointer.To("entry_5")

		query, args := testTables.buildListEntries(dialect.Postgres, nil, filter, 10)

		test.StrContains(t, query, "id > $1")
		test.StrContains(t, query, "ORDER BY id LIMIT $2")
		test.Eq(t, []any{"entry_5", 10}, args)
	})

	T.Run("reverses the cursor comparison when sorting newest first", func(t *testing.T) {
		t.Parallel()

		filter := filtering.DefaultQueryFilter()
		filter.SortBy = filtering.SortDescending
		filter.Cursor = pointer.To("entry_5")

		query, _ := testTables.buildListEntries(dialect.Postgres, nil, filter, 10)

		// The comparison has to follow the order, or the second page walks away
		// from the first instead of continuing it.
		test.StrContains(t, query, "id < $1")
		test.StrContains(t, query, "ORDER BY id DESC")
	})

	T.Run("ignores an empty cursor", func(t *testing.T) {
		t.Parallel()

		filter := filtering.DefaultQueryFilter()
		filter.Cursor = pointer.To("")

		query, args := testTables.buildListEntries(dialect.SQLite, nil, filter, 10)

		test.StrNotContains(t, query, "id >")
		test.SliceLen(t, 1, args)
	})

	T.Run("stands in a tautology when nothing is filtered", func(t *testing.T) {
		t.Parallel()

		query, _ := testTables.buildListEntries(dialect.SQLite, nil, nil, 10)
		test.StrContains(t, query, "WHERE 1=1")
	})
}

func TestQuery_where(T *testing.T) {
	T.Parallel()

	T.Run("renders every selector", func(t *testing.T) {
		t.Parallel()

		q := &Query{
			Scope:         pointer.To("acct_1"),
			ActorID:       "user_1",
			ActorType:     ActorService,
			ResourceID:    "recipe_1",
			ResourceTypes: []string{"recipe", "meal"},
			EventTypes:    []EventType{EventCreated, EventDeleted},
		}

		predicates, args := q.where(dialect.Postgres)
		joined := strings.Join(predicates, " AND ")

		test.StrContains(t, joined, "scope = $1")
		test.StrContains(t, joined, "actor_id = $2")
		test.StrContains(t, joined, "actor_type = $3")
		test.StrContains(t, joined, "resource_id = $4")
		test.StrContains(t, joined, "resource_type IN ($5, $6)")
		test.StrContains(t, joined, "event_type IN ($7, $8)")

		test.Eq(t, []any{
			"acct_1", "user_1", "service", "recipe_1", "recipe", "meal", "created", "deleted",
		}, args)
	})

	T.Run("renders the empty scope as a predicate, not as an absence", func(t *testing.T) {
		t.Parallel()

		predicates, args := (&Query{Scope: pointer.To("")}).where(dialect.SQLite)

		// The distinction a plain string could not express: platform-level
		// events only, rather than every tenant's.
		must.SliceLen(t, 1, predicates)
		test.Eq(t, []any{""}, args)
	})

	T.Run("renders nothing for a nil or zero query", func(t *testing.T) {
		t.Parallel()

		predicates, args := (*Query)(nil).where(dialect.SQLite)
		test.SliceEmpty(t, predicates)
		test.SliceEmpty(t, args)

		predicates, args = (&Query{}).where(dialect.SQLite)
		test.SliceEmpty(t, predicates)
		test.SliceEmpty(t, args)
	})
}

func TestApplyFilterWindow(T *testing.T) {
	T.Parallel()

	T.Run("maps the filter's creation bounds onto recorded_at", func(t *testing.T) {
		t.Parallel()

		after := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
		before := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)

		filter := filtering.DefaultQueryFilter()
		filter.CreatedAfter = &after
		filter.CreatedBefore = &before

		predicates, args := applyFilterWindow(dialect.Postgres, nil, nil, filter)
		joined := strings.Join(predicates, " AND ")

		// So the createdBefore and createdAfter query parameters an HTTP caller
		// already knows how to send mean what they should here.
		test.StrContains(t, joined, "recorded_at > $1")
		test.StrContains(t, joined, "recorded_at < $2")
		test.SliceLen(t, 2, args)
	})

	T.Run("adds nothing for a nil or unbounded filter", func(t *testing.T) {
		t.Parallel()

		predicates, args := applyFilterWindow(dialect.SQLite, nil, nil, nil)
		test.SliceEmpty(t, predicates)
		test.SliceEmpty(t, args)

		predicates, args = applyFilterWindow(dialect.SQLite, nil, nil, filtering.DefaultQueryFilter())
		test.SliceEmpty(t, predicates)
		test.SliceEmpty(t, args)
	})
}

func TestBuildPruneQueries(T *testing.T) {
	T.Parallel()

	T.Run("bounds a sweep by both the oldest entry and the first to survive", func(t *testing.T) {
		t.Parallel()

		cutoff := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)

		query, args := testTables.buildSelectPruneBounds(dialect.Postgres, "acct_1", cutoff)

		// One statement over one index range rather than two, which the CASE is
		// what allows: a second aggregate cannot carry its own WHERE clause.
		test.StrContains(t, query, "MIN(seq)")

		// Strictly greater, because an entry recorded exactly at the cutoff is
		// one this sweep may remove — the same reading the scope listing and
		// the backlog count use.
		test.StrContains(t, query, "MIN(CASE WHEN recorded_at > $1 THEN seq END)")
		test.Eq(t, []any{cutoff, "acct_1"}, args)
	})

	T.Run("reads the first page of scopes without a cursor", func(t *testing.T) {
		t.Parallel()

		cutoff := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)

		query, args := testTables.buildSelectPrunableScopes(dialect.Postgres, cutoff, nil, 100)

		// No cursor predicate at all on the first page. The empty string is a
		// real scope, so "scope > ''" would exclude the one platform-level
		// events are recorded in — forever.
		test.StrContains(t, query, "recorded_at <= $1")
		test.StrNotContains(t, query, "scope >")
		test.StrContains(t, query, "ORDER BY scope LIMIT $2")
		test.Eq(t, []any{cutoff, 100}, args)
	})

	T.Run("advances by keyset on the pages after it", func(t *testing.T) {
		t.Parallel()

		cutoff := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
		after := ""

		query, args := testTables.buildSelectPrunableScopes(dialect.Postgres, cutoff, &after, 100)

		test.StrContains(t, query, "AND scope > $2")
		test.StrContains(t, query, "ORDER BY scope LIMIT $3")
		test.Eq(t, []any{cutoff, "", 100}, args)
	})

	T.Run("bounds the backlog reading rather than counting the table", func(t *testing.T) {
		t.Parallel()

		cutoff := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)

		query, args := testTables.buildCountPrunableEntries(dialect.Postgres, cutoff, 1000)

		// A gauge, not an inventory: the LIMIT is what keeps the reading from
		// being most expensive exactly when the backlog is worst.
		test.StrContains(t, query, "SELECT 1 FROM audit_log_entries WHERE recorded_at <= $1 LIMIT $2")
		test.StrContains(t, query, "AS audit_prune_backlog")
		test.Eq(t, []any{cutoff, 1000}, args)
	})

	T.Run("takes the highest surviving position at or below the boundary", func(t *testing.T) {
		t.Parallel()

		query, args := testTables.buildSelectPruneTarget(dialect.SQLite, "acct_1", 41)

		// At or below, rather than exactly at, so a chain that already has a
		// hole in it still yields a real row to take a watermark from.
		test.StrContains(t, query, "seq <= ?")
		test.StrContains(t, query, "ORDER BY seq DESC LIMIT 1")
		test.Eq(t, []any{"acct_1", int64(41)}, args)
	})

	T.Run("deletes a prefix, never a slice out of the middle", func(t *testing.T) {
		t.Parallel()

		query, args := testTables.buildDeletePruned(dialect.MySQL, "acct_1", 41)

		test.StrContains(t, query, "scope = ? AND seq <= ?")
		test.Eq(t, []any{"acct_1", int64(41)}, args)
	})

	T.Run("records where it pruned to", func(t *testing.T) {
		t.Parallel()

		now := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)

		query, args := testTables.buildUpdateChainPruned(dialect.Postgres, "acct_1", "hash", 41, now)

		test.StrContains(t, query, "pruned_through_seq = $1")
		test.StrContains(t, query, "pruned_through_hash = $2")
		test.Eq(t, []any{int64(41), "hash", now, "acct_1"}, args)
	})
}

func TestNewTables(T *testing.T) {
	T.Parallel()

	T.Run("derives both names from one prefix", func(t *testing.T) {
		t.Parallel()

		tables := newTables("custom")
		test.EqOp(t, "custom_audit_log_entries", tables.entries)
		test.EqOp(t, "custom_audit_log_chains", tables.chains)
	})
}
