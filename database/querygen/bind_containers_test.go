package querygen

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// bind_test.go checks that the bound statements are the emitted ones with a
// different argument spelling, against the rewrite sqlc performs. That is an
// assertion about text, and text is only half of it: the reason this package
// renders a statement at all is that a server executes it, and the bound path
// has a failure mode the sqlc path does not.
//
// A statement rendered here is handed to a driver directly rather than to a
// generator, so the count and order of its arguments are this package's problem
// rather than sqlc's. Getting them wrong does not always fail: a marker too many
// is an error a driver raises, but a value in the wrong slot is a query that
// runs and answers about something else — a cursor compared against a limit, a
// window compared against an account id.
//
// So the bound statements get the same treatment the emitted ones get: run,
// against one of each server, asserting the behavior rather than the SQL. This
// suite hangs off runDialect beside the sqlc one and stands up its own table, so
// neither can see the other's writes.

const gadgetsTable = "gadgets"

const (
	gadgetOwner = "account_one"
	gadgetOther = "account_two"
)

// gadgetStatements is what a scoped store would hold: one statement per
// operation, each keyed on the owner as well as on whatever it already keyed on.
//
// Rendering them once up front is the usage this package is built for — a store
// renders at construction and executes per request — and it is also the thing
// worth checking, since a Bound that only works when rendered per call would
// have hidden the repeated-argument bug rather than exposed it.
type gadgetStatements struct {
	create  Bound
	get     Bound
	exists  Bound
	update  Bound
	archive Bound
	list    Bound
}

func gadgetsFor(d dialect.Dialect) *gadgetStatements {
	var (
		g       = For(d)
		columns = widgetsColumns()
		owner   = Match{Column: BelongsToAccountColumn}
	)

	return &gadgetStatements{
		create: g.BoundCreate(gadgetsTable, ForInsert(columns), nil),
		get:    g.BoundGet(gadgetsTable, columns, owner),
		exists: g.BoundExists(gadgetsTable, columns, owner),
		// The owner is out of the updatable set: a statement that assigns the
		// column it keys on binds one argument to both, and there is no value
		// of it that moves a row anywhere.
		update:  g.BoundUpdate(gadgetsTable, columns, ForUpdate(columns, BelongsToAccountColumn), nil, owner),
		archive: g.BoundArchive(gadgetsTable, owner),
		list:    g.BoundList(gadgetsTable, columns, owner),
	}
}

func (s *gadgetStatements) all() map[string]Bound {
	return map[string]Bound{
		"create":  s.create,
		"get":     s.get,
		"exists":  s.exists,
		"update":  s.update,
		"archive": s.archive,
		"list":    s.list,
	}
}

// execBound binds the values and runs the statement, failing on either.
func execBound(tb testing.TB, ctx context.Context, db *sql.DB, b Bound, values map[string]any) sql.Result {
	tb.Helper()

	arguments, err := b.Bind(values)
	must.NoError(tb, err)

	result, err := db.ExecContext(ctx, b.SQL, arguments...)
	must.NoError(tb, err, must.Sprintf("executing\n%s", b.SQL))

	return result
}

func affectedRows(tb testing.TB, result sql.Result) int64 {
	tb.Helper()

	affected, err := result.RowsAffected()
	must.NoError(tb, err)

	return affected
}

func insertGadget(tb testing.TB, ctx context.Context, db *sql.DB, s *gadgetStatements, id, name, account string) {
	tb.Helper()

	execBound(tb, ctx, db, s.create, map[string]any{
		IDColumn:               id,
		"name":                 name,
		BelongsToAccountColumn: account,
	})
}

// getGadget runs the bound get, returning the name it read and whether there was
// a row at all.
func getGadget(tb testing.TB, ctx context.Context, db *sql.DB, s *gadgetStatements, id, account string) (name string, found bool) {
	tb.Helper()

	arguments, err := s.get.Bind(map[string]any{IDColumn: id, BelongsToAccountColumn: account})
	must.NoError(tb, err)

	var (
		gotID                                     string
		gotAccount                                string
		indexed, created, updated, archived, read any
	)

	err = db.QueryRowContext(ctx, s.get.SQL, arguments...).
		Scan(&gotID, &read, &gotAccount, &indexed, &created, &updated, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}

	must.NoError(tb, err)

	got, ok := read.(string)
	if !ok {
		// MySQL hands a VARCHAR back as bytes.
		raw, isBytes := read.([]byte)
		must.True(tb, isBytes, must.Sprintf("name came back as %T", read))
		got = string(raw)
	}

	return got, true
}

// listGadgets runs the bound list for one account, returning the page and the
// two counts beside it.
func listGadgets(tb testing.TB, ctx context.Context, d dialect.Dialect, db *sql.DB, s *gadgetStatements, account string, filter *filtering.QueryFilter) (ids []string, filtered, total int64) {
	tb.Helper()

	values := map[string]any{BelongsToAccountColumn: account}
	For(d).BindFilter(values, filter)

	arguments, err := s.list.Bind(values)
	must.NoError(tb, err)

	rows, err := db.QueryContext(ctx, s.list.SQL, arguments...)
	must.NoError(tb, err, must.Sprintf("executing\n%s", s.list.SQL))

	defer func() { must.NoError(tb, rows.Close()) }()

	for rows.Next() {
		var (
			id                                               string
			name, owner, indexed, created, updated, archived any
			rowFiltered, rowTotal                            int64
		)

		must.NoError(tb, rows.Scan(&id, &name, &owner, &indexed, &created, &updated, &archived, &rowFiltered, &rowTotal))

		ids = append(ids, id)
		filtered, total = rowFiltered, rowTotal
	}

	must.NoError(tb, rows.Err())

	return ids, filtered, total
}

// runBoundSuite is the bound counterpart of runWidgetSuite, and like it is
// written once and run against each of the three servers.
func runBoundSuite(t *testing.T, ctx context.Context, d dialect.Dialect, db *sql.DB) {
	t.Helper()

	_, err := db.ExecContext(ctx, conventionalDDL(d, gadgetsTable))
	must.NoError(t, err)

	statements := gadgetsFor(d)

	t.Run("every bound statement is one the server accepts", func(t *testing.T) {
		// Unlike the emitted statements, these need no rewriting first: what a
		// Bound holds is what a driver is handed.
		all := statements.all()
		for name := range all {
			stmt, prepareErr := db.PrepareContext(ctx, all[name].SQL)
			must.NoError(t, prepareErr, must.Sprintf("preparing %s:\n%s", name, all[name].SQL))
			must.NoError(t, stmt.Close()) //nolint:sqlclosecheck // the prepare is the assertion; there is nothing to read.
		}
	})

	for _, id := range []string{"g_003", "g_001", "g_004", "g_002"} {
		insertGadget(t, ctx, db, statements, id, "gadget "+id, gadgetOwner)
	}

	insertGadget(t, ctx, db, statements, "g_005", "someone else's gadget", gadgetOther)

	t.Run("a read is answered within its scope and refused outside it", func(t *testing.T) {
		name, found := getGadget(t, ctx, db, statements, "g_001", gadgetOwner)

		test.True(t, found)
		test.EqOp(t, "gadget g_001", name)

		// The row exists; the scope is what withholds it. A bound statement
		// whose extra predicate went missing would return it here, and the
		// caller would never learn it had read across a tenant boundary.
		_, found = getGadget(t, ctx, db, statements, "g_005", gadgetOwner)
		test.False(t, found)
	})

	t.Run("exists reports what get would find", func(t *testing.T) {
		cases := []struct {
			id, account string
			want        bool
		}{
			{"g_001", gadgetOwner, true},
			{"g_005", gadgetOwner, false},
			{"g_005", gadgetOther, true},
			{"g_nope", gadgetOwner, false},
		}

		for i := range cases {
			tc := cases[i]

			arguments, bindErr := statements.exists.Bind(map[string]any{
				IDColumn:               tc.id,
				BelongsToAccountColumn: tc.account,
			})
			must.NoError(t, bindErr)

			var got bool

			must.NoError(t, db.QueryRowContext(ctx, statements.exists.SQL, arguments...).Scan(&got))
			test.EqOp(t, tc.want, got, test.Sprintf("%s in %s", tc.id, tc.account))
		}
	})

	t.Run("the list pages without its counts moving", func(t *testing.T) {
		// filtered_count carries the window and the archived toggle but not the
		// cursor, so it answers "how many are left" rather than "how many are
		// on this page" — and it is the same answer on the fiftieth page as on
		// the first.
		first, filtered, total := listGadgets(t, ctx, d, db, statements, gadgetOwner,
			&filtering.QueryFilter{MaxResponseSize: pointer.To(uint16(2))})

		test.Eq(t, []string{"g_001", "g_002"}, first)
		test.EqOp(t, int64(4), filtered)
		test.EqOp(t, int64(4), total)

		second, filtered, total := listGadgets(t, ctx, d, db, statements, gadgetOwner,
			&filtering.QueryFilter{MaxResponseSize: pointer.To(uint16(2)), Cursor: pointer.To("g_002")})

		test.Eq(t, []string{"g_003", "g_004"}, second)
		test.EqOp(t, int64(4), filtered)
		test.EqOp(t, int64(4), total)
	})

	t.Run("the list counts its own scope and not the table", func(t *testing.T) {
		// The other account's row is in the table and in neither count: a keyed
		// list whose counts were unkeyed would report five here, which reads as
		// a pagination bug somewhere else entirely.
		ids, filtered, total := listGadgets(t, ctx, d, db, statements, gadgetOther, nil)

		test.Eq(t, []string{"g_005"}, ids)
		test.EqOp(t, int64(1), filtered)
		test.EqOp(t, int64(1), total)
	})

	t.Run("a window bound through BindFilter filters", func(t *testing.T) {
		// This is the assertion SQLite needs. Its timestamps are text and its
		// comparisons over them lexicographic, and a time handed to the driver
		// as a time arrives as a number — which its affinity rules sort below
		// every string, so the window admits every row for every bound. The
		// page would look correct, the count would look correct, and the filter
		// would be doing nothing.
		future := time.Now().Add(time.Hour)

		ids, filtered, _ := listGadgets(t, ctx, d, db, statements, gadgetOwner,
			&filtering.QueryFilter{CreatedAfter: &future})

		test.SliceEmpty(t, ids)
		test.EqOp(t, int64(0), filtered)

		past := time.Now().Add(-time.Hour)

		ids, filtered, _ = listGadgets(t, ctx, d, db, statements, gadgetOwner,
			&filtering.QueryFilter{CreatedAfter: &past})

		test.SliceLen(t, 4, ids)
		test.EqOp(t, int64(4), filtered)
	})

	t.Run("an update writes within its scope and no further", func(t *testing.T) {
		values := map[string]any{
			IDColumn:               "g_001",
			"name":                 "renamed",
			BelongsToAccountColumn: gadgetOwner,
		}

		test.EqOp(t, int64(1), affectedRows(t, execBound(t, ctx, db, statements.update, values)))

		name, found := getGadget(t, ctx, db, statements, "g_001", gadgetOwner)
		test.True(t, found)
		test.EqOp(t, "renamed", name)

		// The same statement, aimed at another account's row: no error and no
		// row, which is the only report a caller gets and the reason the update
		// counts rows rather than execing.
		values[IDColumn] = "g_005"

		test.EqOp(t, int64(0), affectedRows(t, execBound(t, ctx, db, statements.update, values)))

		name, found = getGadget(t, ctx, db, statements, "g_005", gadgetOther)
		test.True(t, found)
		test.EqOp(t, "someone else's gadget", name)
	})

	t.Run("archive soft-deletes once and reports the second attempt", func(t *testing.T) {
		values := map[string]any{IDColumn: "g_004", BelongsToAccountColumn: gadgetOwner}

		test.EqOp(t, int64(1), affectedRows(t, execBound(t, ctx, db, statements.archive, values)))

		// The archived_at IS NULL in the WHERE is what makes the second count
		// zero rather than one.
		test.EqOp(t, int64(0), affectedRows(t, execBound(t, ctx, db, statements.archive, values)))

		_, found := getGadget(t, ctx, db, statements, "g_004", gadgetOwner)
		test.False(t, found)
	})

	t.Run("include_archived admits the archived row rather than decorating the query", func(t *testing.T) {
		ids, filtered, _ := listGadgets(t, ctx, d, db, statements, gadgetOwner, nil)
		test.Eq(t, []string{"g_001", "g_002", "g_003"}, ids)
		test.EqOp(t, int64(3), filtered)

		ids, filtered, _ = listGadgets(t, ctx, d, db, statements, gadgetOwner,
			&filtering.QueryFilter{IncludeArchived: pointer.To(true)})

		test.Eq(t, []string{"g_001", "g_002", "g_003", "g_004"}, ids)
		test.EqOp(t, int64(4), filtered)
	})
}
