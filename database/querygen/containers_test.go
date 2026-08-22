package querygen

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/testutils/containers/mysqltest"
	"github.com/primandproper/platform-go/v13/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	_ "modernc.org/sqlite"
)

// The emitted SQL is only worth anything if a server accepts it, and half of
// what this package promises is behavioral rather than textual: that
// include_archived toggles something, that filtered_count does not move as a
// caller pages, that the reindex walk covers every live row exactly once in byte
// order. None of that is visible in a string comparison, so it is checked here,
// against a real server — and, since this package emits three dialects, against
// one of each.
//
// runWidgetSuite is written once and run three times. That is the whole point:
// the promises above are the same promises whichever server keeps them, so a
// dialect whose SQL parses but whose include_archived does nothing fails the
// same assertion Postgres would. What differs per dialect is confined to the
// DDL, to how a bound argument is spelled, and to how a timestamp is handed to a
// driver.
//
// SQLite's round trip is here rather than in a file of its own, next to the two
// it shares a suite with, and it is not gated on RUN_CONTAINER_TESTS: it needs no
// container, only a temporary file.

const widgetsTable = "widgets"

// conventionalDDL is a table with every column this package has an opinion
// about, in each dialect's spelling of it. It takes the table name because the
// bound half of the suite stands its own copy up — see bind_containers_test.go.
//
// The differences are not stylistic. MySQL cannot make a TEXT column a primary
// key without a prefix length, so ids live in a VARCHAR. SQLite has no timestamp
// type at all, and its comparisons over one are lexicographic over text, so the
// columns are TEXT and the default is the CURRENT_TIMESTAMP whose format those
// comparisons assume — which is the schema requirement the package comment
// states and this package cannot enforce.
func conventionalDDL(d dialect.Dialect, table string) string {
	switch d {
	case dialect.MySQL:
		return fmt.Sprintf(`CREATE TABLE %s (
			id VARCHAR(64) NOT NULL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			belongs_to_account VARCHAR(64) NOT NULL,
			last_indexed_at DATETIME NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_updated_at DATETIME NULL,
			archived_at DATETIME NULL
		)`, table)
	case dialect.SQLite:
		return fmt.Sprintf(`CREATE TABLE %s (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL,
			belongs_to_account TEXT NOT NULL,
			last_indexed_at TEXT,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_updated_at TEXT,
			archived_at TEXT
		)`, table)
	// Postgres, which For has already narrowed the alternatives to.
	default:
		return fmt.Sprintf(`CREATE TABLE %s (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL,
			belongs_to_account TEXT NOT NULL,
			last_indexed_at TIMESTAMP WITH TIME ZONE,
			created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
			last_updated_at TIMESTAMP WITH TIME ZONE,
			archived_at TIMESTAMP WITH TIME ZONE
		)`, table)
	}
}

func widgetsColumns() []string {
	return []string{
		IDColumn,
		"name",
		BelongsToAccountColumn,
		LastIndexedAtColumn,
		CreatedAtColumn,
		LastUpdatedAtColumn,
		ArchivedAtColumn,
	}
}

func widgetsQueries(d dialect.Dialect) []*Query {
	return For(d).StandardCRUD(widgetsTable, widgetsColumns(),
		WithEntity("Widget", "Widgets"),
		WithOwnership(BelongsToAccountColumn),
	)
}

// sqlcSlice matches a sqlc.slice reference in emitted SQL.
var sqlcSlice = regexp.MustCompile(`sqlc\.slice\(([a-zA-Z0-9_]+)\)`)

// expandSlices does to a sqlc.slice reference what sqlc does before a driver
// sees one: replaces it with one ordinary argument per element of the value
// bound to it. The per-element names are the set's name and an index, which
// cannot collide with a column name because no identifier this package accepts
// contains a '#'.
//
// It returns a values map with the elements spliced in, so bindArguments and
// argumentsFor need to know nothing about sets.
func expandSlices(tb testing.TB, statement string, values map[string]any) (expandedStatement string, expandedValues map[string]any) {
	tb.Helper()

	expandedValues = make(map[string]any, len(values))
	maps.Copy(expandedValues, values)

	expandedStatement = sqlcSlice.ReplaceAllStringFunc(statement, func(match string) string {
		name := sqlcSlice.FindStringSubmatch(match)[1]

		elements, ok := values[name].([]string)
		must.True(tb, ok, must.Sprintf("statement wants a set named %q", name))

		parts := make([]string, 0, len(elements))

		for i, element := range elements {
			part := fmt.Sprintf("%s#%d", name, i)
			expandedValues[part] = element
			parts = append(parts, "sqlc.arg("+part+")")
		}

		return strings.Join(parts, ", ")
	})

	return expandedStatement, expandedValues
}

// argumentsFor lines values up with the placeholders bindArguments produced,
// failing the test when the statement wants an argument the caller did not
// supply.
func argumentsFor(tb testing.TB, order []string, values map[string]any) []any {
	tb.Helper()

	out := make([]any, 0, len(order))

	for _, name := range order {
		value, ok := values[name]
		must.True(tb, ok, must.Sprintf("statement wants an argument named %q", name))
		out = append(out, value)
	}

	return out
}

// widgetQuery finds one of the generated queries and returns it bound, with the
// values its sets expanded into ordinary arguments.
func widgetQuery(tb testing.TB, d dialect.Dialect, name string, values map[string]any) (statement string, arguments []any) {
	tb.Helper()

	rewritten, expanded := expandSlices(tb, named(tb, widgetsQueries(d), name).Content, values)
	bound, order := bindArguments(d, rewritten)

	return bound, argumentsFor(tb, order, expanded)
}

// timeArg renders a time the way d's driver wants to receive one.
//
// Postgres and MySQL both have a timestamp type and drivers that speak
// time.Time. SQLite has neither, so a time is handed over already in the text
// shape its stored timestamps use — the same shape CURRENT_TIMESTAMP writes,
// since a comparison between two texts in different shapes is a comparison in an
// order that is not chronological.
func timeArg(d dialect.Dialect, at time.Time) any {
	if d == dialect.SQLite {
		return at.UTC().Format(time.DateTime)
	}

	return at
}

// filterDefaults is a QueryFilter that filters nothing: every window unset,
// archived excluded, the first page.
//
// The limit is set rather than left nil, because the emitted LIMIT binds a
// required argument on every dialect — see limitClause. This map is the caller
// side of filtering.QueryFilter.Normalize.
func filterDefaults() map[string]any {
	return map[string]any{
		CreatedAfterArg:    nil,
		CreatedBeforeArg:   nil,
		UpdatedAfterArg:    nil,
		UpdatedBeforeArg:   nil,
		IncludeArchivedArg: nil,
		CursorArg:          nil,
		LimitArg:           50,
	}
}

const testAccount = "account_one"

// insertWidget runs the generated create statement.
func insertWidget(tb testing.TB, ctx context.Context, d dialect.Dialect, db *sql.DB, id, name, account string) {
	tb.Helper()

	statement, arguments := widgetQuery(tb, d, "CreateWidget", map[string]any{
		IDColumn:               id,
		"name":                 name,
		BelongsToAccountColumn: account,
	})

	_, err := db.ExecContext(ctx, statement, arguments...)
	must.NoError(tb, err)
}

// scanIDs runs a statement whose rows are a single id column.
func scanIDs(tb testing.TB, ctx context.Context, db *sql.DB, statement string, arguments []any) []string {
	tb.Helper()

	rows, err := db.QueryContext(ctx, statement, arguments...)
	must.NoError(tb, err)

	defer func() { must.NoError(tb, rows.Close()) }()

	var ids []string

	for rows.Next() {
		var id string

		must.NoError(tb, rows.Scan(&id))

		ids = append(ids, id)
	}

	must.NoError(tb, rows.Err())

	return ids
}

// listWidgets runs the generated list statement, returning the ids it read and
// the two counts it reported.
//
// Every column but the id and the counts is scanned into an any. The three
// drivers hand back a stored timestamp as three different Go types — SQLite's as
// the text it is — and none of this suite's assertions is about which, so
// insisting on one would be asserting about the driver rather than about the
// SQL.
func listWidgets(tb testing.TB, ctx context.Context, d dialect.Dialect, db *sql.DB, values map[string]any) (ids []string, filtered, total int64) {
	tb.Helper()

	statement, arguments := widgetQuery(tb, d, "ListWidgets", values)

	rows, err := db.QueryContext(ctx, statement, arguments...)
	must.NoError(tb, err)

	defer func() { must.NoError(tb, rows.Close()) }()

	for rows.Next() {
		var (
			id                                                 string
			name, account, indexed, created, updated, archived any
			rowFiltered, rowTotal                              int64
		)

		must.NoError(tb, rows.Scan(&id, &name, &account, &indexed, &created, &updated, &archived, &rowFiltered, &rowTotal))

		ids = append(ids, id)
		filtered, total = rowFiltered, rowTotal
	}

	must.NoError(tb, rows.Err())

	return ids, filtered, total
}

func TestQuerygen_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		runDialect(T, ctx, dialect.Postgres, pg.DB)
	})
}

func TestQuerygen_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		runDialect(T, ctx, dialect.MySQL, my.DB)
	})
}

//nolint:tparallel // the suite is sequential against a shared table, deliberately.
func TestQuerygen_SQLite(T *testing.T) {
	T.Parallel()

	// A file rather than :memory:, and one connection rather than a pool:
	// SQLite gives each connection to an in-memory database its own database,
	// and is a single writer besides.
	db, err := sql.Open("sqlite", filepath.Join(T.TempDir(), "querygen.db"))
	must.NoError(T, err)

	T.Cleanup(func() { must.NoError(T, db.Close()) })

	db.SetMaxOpenConns(1)

	runDialect(T, T.Context(), dialect.SQLite, db)
}

// runDialect stands the table up and runs both halves of the check against it:
// that every emitted statement is one the server accepts, and that the ones that
// promise a behavior deliver it.
func runDialect(t *testing.T, ctx context.Context, d dialect.Dialect, db *sql.DB) {
	t.Helper()

	_, err := db.ExecContext(ctx, conventionalDDL(d, widgetsTable))
	must.NoError(t, err)

	// Neither subtest is parallel, and the suite's own children are not
	// either: they share one table and one another's writes are what they
	// assert against.
	t.Run("every generated statement is one the server accepts", func(t *testing.T) {
		for _, query := range widgetsQueries(d) {
			prepare(t, ctx, d, db, query)
		}
	})

	t.Run("the suite", func(t *testing.T) {
		runWidgetSuite(t, ctx, d, db)
	})

	// The bound statements against their own table — see bind_containers_test.go.
	t.Run("the bound suite", func(t *testing.T) {
		runBoundSuite(t, ctx, d, db)
	})
}

// prepare asks the server to plan the statement, which is the cheapest way to
// learn that every column it names exists and every argument it binds has a type
// the server can infer.
//
// A set is expanded to one element first. sqlc.slice has no meaning to a server
// — it is a macro sqlc expands per call, since the arity is the caller's — so
// preparing the unexpanded text would be preparing something no driver ever
// sends.
func prepare(tb testing.TB, ctx context.Context, d dialect.Dialect, db *sql.DB, query *Query) {
	tb.Helper()

	expanded, _ := expandSlices(tb, query.Content, map[string]any{IDsArg: []string{"one"}})
	statement, _ := bindArguments(d, expanded)

	stmt, err := db.PrepareContext(ctx, statement)
	must.NoError(tb, err, must.Sprintf("preparing %s", query.Annotation.Name))
	must.NoError(tb, stmt.Close()) //nolint:sqlclosecheck // the prepare is the assertion; there is nothing to read.
}

func runWidgetSuite(t *testing.T, ctx context.Context, d dialect.Dialect, db *sql.DB) {
	t.Helper()

	// Ids are compared as bytes by the reindex walk, so they are inserted out of
	// order to make sure nothing is relying on insertion order.
	for _, id := range []string{"w_003", "w_001", "w_004", "w_002"} {
		insertWidget(t, ctx, d, db, id, "widget "+id, testAccount)
	}

	insertWidget(t, ctx, d, db, "w_005", "someone else's widget", "account_two")

	t.Run("archive soft-deletes once and reports the second attempt", func(t *testing.T) {
		statement, arguments := widgetQuery(t, d, "ArchiveWidget", map[string]any{
			IDColumn:               "w_004",
			BelongsToAccountColumn: testAccount,
		})

		result, err := db.ExecContext(ctx, statement, arguments...)
		must.NoError(t, err)

		affected, err := result.RowsAffected()
		must.NoError(t, err)
		test.EqOp(t, int64(1), affected)

		// The archived_at IS NULL in the WHERE is what makes this second count
		// zero rather than one, and it is the only thing telling a caller their
		// archival was a no-op.
		result, err = db.ExecContext(ctx, statement, arguments...)
		must.NoError(t, err)

		affected, err = result.RowsAffected()
		must.NoError(t, err)
		test.EqOp(t, int64(0), affected)
	})

	t.Run("get and exists refuse a row belonging to another account", func(t *testing.T) {
		getStatement, getArguments := widgetQuery(t, d, "GetWidget", map[string]any{
			IDColumn:               "w_005",
			BelongsToAccountColumn: testAccount,
		})

		var id string
		test.ErrorIs(t, db.QueryRowContext(ctx, getStatement, getArguments...).Scan(&id), sql.ErrNoRows)

		existsStatement, existsArguments := widgetQuery(t, d, "CheckWidgetExistence", map[string]any{
			IDColumn:               "w_005",
			BelongsToAccountColumn: testAccount,
		})

		var exists bool
		must.NoError(t, db.QueryRowContext(ctx, existsStatement, existsArguments...).Scan(&exists))
		test.False(t, exists)
	})

	t.Run("include_archived actually includes archived rows", func(t *testing.T) {
		values := filterDefaults()
		values[BelongsToAccountColumn] = testAccount

		excluded, _, _ := listWidgets(t, ctx, d, db, values)
		test.Eq(t, []string{"w_001", "w_002", "w_003"}, excluded)

		values[IncludeArchivedArg] = true

		included, _, _ := listWidgets(t, ctx, d, db, values)
		test.Eq(t, []string{"w_001", "w_002", "w_003", "w_004"}, included)
	})

	t.Run("the counts describe the filter rather than the page", func(t *testing.T) {
		values := filterDefaults()
		values[BelongsToAccountColumn] = testAccount
		values[LimitArg] = 2

		first, filtered, total := listWidgets(t, ctx, d, db, values)
		test.Eq(t, []string{"w_001", "w_002"}, first)
		test.EqOp(t, int64(3), filtered)
		test.EqOp(t, int64(3), total)

		values[CursorArg] = first[len(first)-1]

		second, filteredAgain, totalAgain := listWidgets(t, ctx, d, db, values)
		test.Eq(t, []string{"w_003"}, second)

		// A count that moved with the cursor would report two here, then one,
		// then zero — a total that empties itself as the caller reads it.
		test.EqOp(t, filtered, filteredAgain)
		test.EqOp(t, total, totalAgain)
	})

	t.Run("an absent page size falls back where the dialect can express a fallback", func(t *testing.T) {
		// Postgres and SQLite coalesce an unset limit to filtering's default.
		// MySQL cannot — its LIMIT takes a placeholder and nothing else — so
		// there is nothing to check there beyond what the prepare already
		// proved, which is that the statement parses.
		if d == dialect.MySQL {
			t.Skip("MySQL binds the page size rather than defaulting it; see Generator.limitClause")
		}

		values := filterDefaults()
		values[BelongsToAccountColumn] = testAccount
		values[LimitArg] = nil

		ids, _, _ := listWidgets(t, ctx, d, db, values)

		test.Eq(t, []string{"w_001", "w_002", "w_003"}, ids)
	})

	t.Run("the keyset walk reads every row exactly once", func(t *testing.T) {
		values := filterDefaults()
		values[BelongsToAccountColumn] = testAccount
		values[LimitArg] = 1

		var walked []string

		for range 10 {
			page, _, _ := listWidgets(t, ctx, d, db, values)
			if len(page) == 0 {
				break
			}

			walked = append(walked, page...)
			values[CursorArg] = page[len(page)-1]
		}

		test.Eq(t, []string{"w_001", "w_002", "w_003"}, walked)
	})

	t.Run("the created window bounds the page and the count together", func(t *testing.T) {
		values := filterDefaults()
		values[BelongsToAccountColumn] = testAccount
		values[CreatedAfterArg] = timeArg(d, time.Now().Add(time.Hour))

		ids, filtered, total := listWidgets(t, ctx, d, db, values)

		test.SliceEmpty(t, ids)
		// Both counts ride on the rows, so an empty page carries neither. These
		// zeroes are the scan finding nothing to scan, not an answer: a caller
		// turning this page into a response reports it with
		// filtering.NewQueryFilteredResultWithoutCounts rather than passing the
		// zeroes on as counts.
		test.EqOp(t, int64(0), filtered)
		test.EqOp(t, int64(0), total)

		// Widen the window back and the pair is legible again: everything
		// matched, out of everything.
		values[CreatedAfterArg] = nil

		ids, filtered, total = listWidgets(t, ctx, d, db, values)

		test.SliceLen(t, 3, ids)
		test.EqOp(t, int64(3), filtered)
		test.EqOp(t, int64(3), total)
	})

	t.Run("update stamps last_updated_at and leaves the owner alone", func(t *testing.T) {
		statement, arguments := widgetQuery(t, d, "UpdateWidget", map[string]any{
			IDColumn:               "w_001",
			"name":                 "renamed",
			BelongsToAccountColumn: testAccount,
		})

		result, err := db.ExecContext(ctx, statement, arguments...)
		must.NoError(t, err)

		affected, err := result.RowsAffected()
		must.NoError(t, err)
		test.EqOp(t, int64(1), affected)

		var (
			name    string
			account string
			updated any
		)

		must.NoError(t, db.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT name, belongs_to_account, last_updated_at FROM widgets WHERE id = %s", d.Placeholder(1)), "w_001",
		).Scan(&name, &account, &updated))

		test.EqOp(t, "renamed", name)
		test.EqOp(t, testAccount, account)
		test.NotNil(t, updated)
	})

	t.Run("the reindex scan walks live ids in byte order", func(t *testing.T) {
		var walked []string

		cursor := ""

		for range 10 {
			statement, arguments := widgetQuery(t, d, "ScanWidgetIDsForReindex", map[string]any{
				CursorArg: cursor,
				LimitArg:  2,
			})

			page := scanIDs(t, ctx, db, statement, arguments)
			if len(page) == 0 {
				break
			}

			walked = append(walked, page...)
			cursor = page[len(page)-1]
		}

		// Every unarchived row, from both accounts — a reindex is not scoped —
		// and w_004 absent because it was archived.
		test.Eq(t, []string{"w_001", "w_002", "w_003", "w_005"}, walked)
	})

	t.Run("the stamp marks every id it is handed and nothing else", func(t *testing.T) {
		// Two rows from one account and one from another, in one statement:
		// the stamp is the sync's own machinery and is not scoped to an owner.
		statement, arguments := widgetQuery(t, d, "MarkWidgetsAsIndexed", map[string]any{
			IDsArg: []string{"w_001", "w_003", "w_005"},
		})

		result, err := db.ExecContext(ctx, statement, arguments...)
		must.NoError(t, err)

		affected, err := result.RowsAffected()
		must.NoError(t, err)
		test.EqOp(t, int64(3), affected)

		stamped := scanIDs(t, ctx, db, fmt.Sprintf(
			"SELECT %s FROM %s WHERE %s IS NOT NULL ORDER BY %s",
			IDColumn, widgetsTable, LastIndexedAtColumn, IDColumn), nil)

		test.Eq(t, []string{"w_001", "w_003", "w_005"}, stamped)
	})

	t.Run("the stamp reports the ids that matched no row", func(t *testing.T) {
		// A row deleted outright between the index write and the flush is the
		// case this reports, and the count is all there is to report it with —
		// which is why the query is :execrows rather than :exec.
		statement, arguments := widgetQuery(t, d, "MarkWidgetsAsIndexed", map[string]any{
			IDsArg: []string{"w_002", "w_nonexistent"},
		})

		result, err := db.ExecContext(ctx, statement, arguments...)
		must.NoError(t, err)

		affected, err := result.RowsAffected()
		must.NoError(t, err)
		test.EqOp(t, int64(1), affected)
	})

	t.Run("create refuses to supply a database-owned column", func(t *testing.T) {
		// Nothing here asserts on SQL text: the point is that the generated
		// insert leaves created_at to the server, so a row exists with a
		// creation time nobody passed in.
		insertWidget(t, ctx, d, db, "w_006", "later", testAccount)

		var created any
		must.NoError(t, db.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT %s FROM %s WHERE %s = %s", CreatedAtColumn, widgetsTable, IDColumn, d.Placeholder(1)), "w_006",
		).Scan(&created))

		test.NotNil(t, created)
	})

	t.Run("the search condition matches without regard to case", func(t *testing.T) {
		// ContainsCondition is the one fragment whose dialects reach the same
		// answer by different means — an operator that folds on Postgres, and
		// both sides folded explicitly on the other two. What it promises is
		// this, so this is what is checked against a server rather than against
		// a string.
		statement, order := bindArguments(d, fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s",
			IDColumn, widgetsTable,
			For(d).ContainsCondition(Qualify(widgetsTable, "name"), "name_query"),
			IDColumn))

		found := scanIDs(t, ctx, db, statement,
			argumentsFor(t, order, map[string]any{"name_query": "SOMEONE ELSE"}))

		test.Eq(t, []string{"w_005"}, found)
	})
}
