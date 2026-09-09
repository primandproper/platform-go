package retention

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	_ "modernc.org/sqlite"
)

// This suite is what stands in for the corpus this package cannot have.
//
// Everywhere else in this module a statement is rendered into a canonical .sql,
// checked by sqlc against a committed schema, and executed through a generated
// querier — so nobody has to run it to find out whether the server accepts it.
// Table's two statements name a table a Policy supplies at run time, against a
// schema this module does not ship, so there is nothing for sqlc to check them
// against and no generated method to execute them through. The package doc says
// why that is a ruling; this file is what it costs.
//
// A server is the only thing left that can say the statements are legal, and
// both of them differ by dialect: the delete is two grammars — the native
// bounded write MySQL has and the bounded read the other two are left with —
// and the backlog count rides on a derived table that Postgres and MySQL both
// require an alias for. Until this file existed the MySQL arm had never met a
// MySQL, which is the hazard in a package whose statements delete other
// packages' rows.
//
// runTargetSuite is written once and run three times, because what the
// statements promise is the same on each server: the cutoff is inclusive, the
// bound is honored, the oldest rows go first, a NULL timestamp never expires,
// the key column is the one the policy named, and the backlog saturates. What
// differs per dialect is confined to the DDL. SQLite is here beside the two
// containers rather than in a file of its own, and it is not gated on
// RUN_CONTAINER_TESTS: it needs a temporary file rather than a daemon.

// capturesTable is the table this suite sweeps. It belongs to nobody — which is
// the premise: retention deletes from tables an application owns, and this one
// stands in for the request captures the package doc names.
const capturesTable = "captures"

// capturesDDL is that table in each dialect's spelling.
//
// The differences are the ones this module records elsewhere and none of its
// own. MySQL cannot make a TEXT column a primary key without a prefix length,
// so the key columns are VARCHAR. SQLite has no timestamp type; the column type
// is advisory there, and what decides whether a comparison is chronological is
// the format the driver writes — which is why every instant this suite binds
// goes in as a time.Time, exactly as Table binds its cutoff.
func capturesDDL(d dialect.Dialect) string {
	switch d {
	case dialect.MySQL:
		return fmt.Sprintf(`CREATE TABLE %s (
			id VARCHAR(64) NOT NULL PRIMARY KEY,
			token VARCHAR(64) NOT NULL UNIQUE,
			recorded_at DATETIME(6) NOT NULL,
			expires_at DATETIME(6) NULL
		)`, capturesTable)
	case dialect.SQLite:
		return fmt.Sprintf(`CREATE TABLE %s (
			id TEXT NOT NULL PRIMARY KEY,
			token TEXT NOT NULL UNIQUE,
			recorded_at DATETIME NOT NULL,
			expires_at DATETIME
		)`, capturesTable)
	// Postgres, which the callers below have already narrowed the alternatives
	// to.
	default:
		return fmt.Sprintf(`CREATE TABLE %s (
			id TEXT NOT NULL PRIMARY KEY,
			token TEXT NOT NULL UNIQUE,
			recorded_at TIMESTAMP WITH TIME ZONE NOT NULL,
			expires_at TIMESTAMP WITH TIME ZONE
		)`, capturesTable)
	}
}

// capture is one row this suite writes, named so a test can say which cohort a
// batch took.
type capture struct {
	recordedAt time.Time
	expiresAt  *time.Time
	id         string
}

// seedCaptures empties the table and writes rows, so each subtest starts from a
// state it named rather than from whatever the one before it left.
func seedCaptures(tb testing.TB, ctx context.Context, d dialect.Dialect, db *sql.DB, captures ...capture) {
	tb.Helper()

	_, err := db.ExecContext(ctx, "DELETE FROM "+capturesTable)
	must.NoError(tb, err)

	statement := fmt.Sprintf("INSERT INTO %s (id, token, recorded_at, expires_at) VALUES (%s)",
		capturesTable, d.Placeholders(1, 4))

	for i := range captures {
		c := &captures[i]

		var expires any
		if c.expiresAt != nil {
			expires = c.expiresAt.UTC()
		}

		_, err = db.ExecContext(ctx, statement, c.id, "token-"+c.id, c.recordedAt.UTC(), expires)
		must.NoError(tb, err, must.Sprintf("inserting %q", c.id))
	}
}

// agedCaptures writes n rows a minute apart ending at the given instant, oldest
// first, with ids that sort in the same order.
func agedCaptures(prefix string, newest time.Time, n int) []capture {
	captures := make([]capture, 0, n)

	for i := range n {
		captures = append(captures, capture{
			id:         prefix + "-" + strconv.Itoa(i),
			recordedAt: newest.Add(time.Duration(i-n+1) * time.Minute),
		})
	}

	return captures
}

// survivingCaptures reads back what a pass left, in the order it would drain
// them next time.
func survivingCaptures(tb testing.TB, ctx context.Context, db *sql.DB) []string {
	tb.Helper()

	rows, err := db.QueryContext(ctx, "SELECT id FROM "+capturesTable+" ORDER BY recorded_at, id")
	must.NoError(tb, err)

	defer func() { must.NoError(tb, rows.Close()) }()

	ids := []string{}

	for rows.Next() {
		var id string

		must.NoError(tb, rows.Scan(&id))

		ids = append(ids, id)
	}

	must.NoError(tb, rows.Err())

	return ids
}

// sweep runs one batch through the same method the Sweeper calls, so what the
// server sees here is what it sees in production.
func sweep(tb testing.TB, ctx context.Context, d dialect.Dialect, db *sql.DB, target Table, cutoff time.Time, limit int) int64 {
	tb.Helper()

	removed, err := target.Sweep(ctx, database.NewTxForTesting(db), d, cutoff, limit)
	must.NoError(tb, err)

	return removed
}

func TestTargetStatements_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		runTargetSuite(T, ctx, dialect.Postgres, pg.DB)
	})
}

func TestTargetStatements_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		runTargetSuite(T, ctx, dialect.MySQL, my.DB)
	})
}

//nolint:tparallel // the suite is sequential against a shared table, deliberately.
func TestTargetStatements_SQLite(T *testing.T) {
	T.Parallel()

	// A file rather than :memory:, and one connection rather than a pool:
	// modernc.org/sqlite gives each connection to an in-memory database its own
	// database, and SQLite is a single writer besides.
	db, err := sql.Open("sqlite", filepath.Join(T.TempDir(), "retention.db"))
	must.NoError(T, err)

	T.Cleanup(func() { must.NoError(T, db.Close()) })

	db.SetMaxOpenConns(1)

	runTargetSuite(T, T.Context(), dialect.SQLite, db)
}

// runTargetSuite stands the table up and asserts what Table's two statements
// promise, against a server that has the final say about both.
//
// The subtests are sequential and deliberately so: they share one table, and
// each reseeds it to the state it is about to assert on.
func runTargetSuite(t *testing.T, ctx context.Context, d dialect.Dialect, db *sql.DB) {
	t.Helper()

	_, err := db.ExecContext(ctx, capturesDDL(d))
	must.NoError(t, err)

	byRecordedAt := Table{Name: capturesTable, Column: "recorded_at"}

	t.Run("the cutoff is inclusive and nothing past it is touched", func(t *testing.T) {
		seedCaptures(t, ctx, d, db,
			capture{id: "before", recordedAt: baseTime.Add(-time.Minute)},
			capture{id: "at", recordedAt: baseTime},
			capture{id: "after", recordedAt: baseTime.Add(time.Minute)},
		)

		// At or before, not before: a policy's Age is the last instant a row is
		// kept, and a row sitting exactly on the boundary has reached it.
		test.EqOp(t, int64(2), sweep(t, ctx, d, db, byRecordedAt, baseTime, 100))
		test.Eq(t, []string{"after"}, survivingCaptures(t, ctx, db))
	})

	t.Run("a batch honors its bound and takes the oldest rows first", func(t *testing.T) {
		seedCaptures(t, ctx, d, db, agedCaptures("c", baseTime, 5)...)

		// Two of five, and the two oldest — which is what makes successive
		// batches monotonic progress through a backlog rather than an arbitrary
		// two rows from anywhere in it. Nothing but a server can say the ORDER
		// BY survived the shape each dialect renders it in.
		test.EqOp(t, int64(2), sweep(t, ctx, d, db, byRecordedAt, baseTime, 2))
		test.Eq(t, []string{"c-2", "c-3", "c-4"}, survivingCaptures(t, ctx, db))

		test.EqOp(t, int64(2), sweep(t, ctx, d, db, byRecordedAt, baseTime, 2))
		test.Eq(t, []string{"c-4"}, survivingCaptures(t, ctx, db))

		// The short batch is how the Sweeper learns a policy has drained, so it
		// has to be short for the right reason: one row left, one row taken.
		test.EqOp(t, int64(1), sweep(t, ctx, d, db, byRecordedAt, baseTime, 2))
		test.Eq(t, []string{}, survivingCaptures(t, ctx, db))
	})

	t.Run("a row with no timestamp never expires", func(t *testing.T) {
		expired := baseTime.Add(-time.Hour)

		seedCaptures(t, ctx, d, db,
			capture{id: "unbounded", recordedAt: expired},
			capture{id: "bounded", recordedAt: expired, expiresAt: &expired},
		)

		// A comparison against NULL is never true on any of the three, and the
		// reading matters more than the mechanism: a row that never recorded
		// when it stops being useful cannot be aged out on that basis.
		removed := sweep(t, ctx, d, db, Table{Name: capturesTable, Column: "expires_at"}, baseTime, 100)
		test.EqOp(t, int64(1), removed)
		test.Eq(t, []string{"unbounded"}, survivingCaptures(t, ctx, db))
	})

	t.Run("a batch is bounded by the key column the policy named", func(t *testing.T) {
		seedCaptures(t, ctx, d, db, agedCaptures("k", baseTime, 3)...)

		// KeyColumn is what a bounded delete addresses its rows through on the
		// two dialects that bound a read, so a key that is not the primary key
		// has to work — any column that uniquely identifies a row will do. On
		// MySQL the native arm names no key at all, and that this asserts the
		// same rows there is the point: one signature, three renderings.
		byToken := Table{Name: capturesTable, Column: "recorded_at", KeyColumn: "token"}

		test.EqOp(t, int64(2), sweep(t, ctx, d, db, byToken, baseTime, 2))
		test.Eq(t, []string{"k-2"}, survivingCaptures(t, ctx, db))
	})

	t.Run("the backlog counts what remains and saturates at the ceiling", func(t *testing.T) {
		seedCaptures(t, ctx, d, db, append(
			agedCaptures("expired", baseTime, 4),
			agedCaptures("live", baseTime.Add(time.Hour), 3)...,
		)...)

		backlog, backlogErr := byRecordedAt.Backlog(ctx, db, d, baseTime, 1000)
		must.NoError(t, backlogErr)
		test.EqOp(t, int64(4), backlog)

		// The derived table is what keeps the cost of the reading from growing
		// with the size of the problem it reports, and its alias is what
		// Postgres and MySQL both refuse the statement without.
		backlog, backlogErr = byRecordedAt.Backlog(ctx, db, d, baseTime, 2)
		must.NoError(t, backlogErr)
		test.EqOp(t, int64(2), backlog)

		// Sampled after a failing pass as well as a successful one, which is the
		// case the Sweeper actually depends on: a policy that just failed is the
		// one whose backlog somebody needs to see.
		backlog, backlogErr = byRecordedAt.Backlog(ctx, db, d, baseTime.Add(-2*time.Hour), 1000)
		must.NoError(t, backlogErr)
		test.EqOp(t, int64(0), backlog)
	})

	t.Run("a schema-qualified name reaches the same table", func(t *testing.T) {
		if d != dialect.Postgres {
			t.Skip("only Postgres resolves the qualifier this package's identifier rule admits")
		}

		seedCaptures(t, ctx, d, db, agedCaptures("q", baseTime, 2)...)

		// Table.Name admits one dot, and this is the reading of it: the
		// qualifier is a schema. MySQL reads it as a database and SQLite as an
		// attached one, so what it resolves to is the server's business — but
		// the statement has to render legally wherever a policy writes one.
		qualified := Table{Name: "public." + capturesTable, Column: "recorded_at"}

		test.EqOp(t, int64(2), sweep(t, ctx, d, db, qualified, baseTime, 100))
		test.Eq(t, []string{}, survivingCaptures(t, ctx, db))
	})
}
