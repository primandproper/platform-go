package collapsedruns_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	auditmigrations "github.com/primandproper/platform-go/v14/audit/migrations"
	refreshtokensmigrations "github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// collapse is one package whose v14 versioned run became a single schema.
type collapse struct {
	// statements is the package's schema as it stands.
	statements func(d dialect.Dialect, prefix string) ([]string, error)
	// run names the testdata directory holding the frozen v14 run.
	run string
	// component is what the run's errors name, as the package's own did.
	component string
	// versions is how many versions the run shipped.
	versions int
}

var collapses = []collapse{
	{run: "audit", component: "audit", versions: 2, statements: auditmigrations.Statements},
	{run: "refreshtokens", component: "sign-in refresh token", versions: 4, statements: refreshtokensmigrations.Statements},
}

// runOf rebuilds the v14 run from testdata, version 1 onwards.
func runOf(t *testing.T, c collapse) ddl.Migrations {
	t.Helper()

	read := func(d string, version int) string {
		body, err := os.ReadFile(filepath.Join("testdata", c.run, fmt.Sprintf("%s_v%d.sql", d, version)))
		must.NoError(t, err)

		return string(body)
	}

	run := make(ddl.Migrations, 0, c.versions)

	for version := 1; version <= c.versions; version++ {
		run = append(run, ddl.Migration{Version: uint64(version), Schema: ddl.Schema{
			Component: c.component,
			Postgres:  read("postgres", version),
			MySQL:     read("mysql", version),
			SQLite:    read("sqlite", version),
		}})
	}

	must.NoError(t, run.Validate())

	return run
}

// executor is what applying DDL and reading the catalog need, which a *sql.DB
// and a database.Client's Writer both are.
type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func apply(t *testing.T, db executor, stmts []string) {
	t.Helper()

	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, err := db.ExecContext(t.Context(), stmt)
		must.NoError(t, err, must.Sprintf("executing %s", stmt))
	}
}

// shape is what a database reports about the tables under one prefix, with the
// prefix taken out so two prefixes' tables compare.
type shape struct {
	tables  []string
	columns []string
	indexes []string
}

// rowsOf reads every row of query as one string per row, its columns joined.
func rowsOf(t *testing.T, db executor, query string, args ...any) []string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), query, args...)
	must.NoError(t, err, must.Sprintf("querying %s", query))

	defer func() { must.NoError(t, rows.Close()) }()

	columns, err := rows.Columns()
	must.NoError(t, err)

	var out []string

	for rows.Next() {
		values := make([]sql.NullString, len(columns))
		dest := make([]any, len(values))

		for i := range values {
			dest[i] = &values[i]
		}

		must.NoError(t, rows.Scan(dest...))

		fields := make([]string, len(values))
		for i, v := range values {
			fields[i] = v.String
			if !v.Valid {
				fields[i] = "NULL"
			}
		}

		out = append(out, strings.Join(fields, " | "))
	}

	must.NoError(t, rows.Err())

	return out
}

// shapeOf reads the shape of every table under prefix from the dialect's own
// catalog: the tables, then each one's columns in ordinal order, then each
// one's indexes by name.
func shapeOf(t *testing.T, db executor, d dialect.Dialect, prefix string) shape {
	t.Helper()

	var s shape

	pattern := prefix + `\_%`

	switch d {
	case dialect.SQLite:
		s.tables = rowsOf(t, db, `SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE ? ESCAPE '\' ORDER BY name`, pattern)
		for _, table := range s.tables {
			s.columns = append(s.columns, rowsOf(t, db,
				`SELECT ?, cid, name, type, "notnull", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid`, table, table)...)
			s.indexes = append(s.indexes, rowsOf(t, db,
				`SELECT il.name, il."unique", il.origin,
				        (SELECT group_concat(name, ',') FROM (SELECT name FROM pragma_index_info(il.name) ORDER BY seqno))
				   FROM pragma_index_list(?) AS il ORDER BY il.name`, table)...)
		}
	case dialect.Postgres:
		s.tables = rowsOf(t, db, `SELECT table_name FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name LIKE $1 ORDER BY table_name`, pattern)
		for _, table := range s.tables {
			s.columns = append(s.columns, rowsOf(t, db, `SELECT table_name, ordinal_position, column_name, data_type,
				       is_nullable, column_default
				  FROM information_schema.columns
				 WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position`, table)...)
			s.indexes = append(s.indexes, rowsOf(t, db, `SELECT indexname, indexdef FROM pg_indexes
				WHERE schemaname = current_schema() AND tablename = $1 ORDER BY indexname`, table)...)
		}
	case dialect.MySQL:
		s.tables = rowsOf(t, db, `SELECT TABLE_NAME FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME LIKE ? ORDER BY TABLE_NAME`, pattern)
		for _, table := range s.tables {
			s.columns = append(s.columns, rowsOf(t, db, `SELECT TABLE_NAME, ORDINAL_POSITION, COLUMN_NAME, COLUMN_TYPE,
				       IS_NULLABLE, COLUMN_DEFAULT, EXTRA
				  FROM information_schema.COLUMNS
				 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, table)...)
			s.indexes = append(s.indexes, rowsOf(t, db, `SELECT INDEX_NAME, NON_UNIQUE, SEQ_IN_INDEX, COLUMN_NAME
				  FROM information_schema.STATISTICS
				 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY INDEX_NAME, SEQ_IN_INDEX`, table)...)
		}
	default:
		t.Fatalf("no catalog reading for dialect %q", d)
	}

	must.SliceNotEmpty(t, s.tables, must.Sprintf("no tables under %q", prefix))

	strip := func(rows []string) []string {
		out := make([]string, len(rows))
		for i, row := range rows {
			out[i] = strings.ReplaceAll(row, prefix+"_", "")
		}

		return out
	}

	return shape{tables: strip(s.tables), columns: strip(s.columns), indexes: strip(s.indexes)}
}

// runCollapses is the whole claim, for one dialect against one database: the
// schema creates what the run created, and is a no-op over what the run left.
func runCollapses(t *testing.T, db executor, d dialect.Dialect) {
	t.Helper()

	for _, c := range collapses {
		t.Run(c.run, func(t *testing.T) {
			// The run and the schema share the database under two prefixes,
			// so the catalog answers for both in the same terms.
			fromRun, collapsed := "fromrun"+c.run, "collapsed"+c.run

			runStmts, err := runOf(t, c).Statements(d, fromRun)
			must.NoError(t, err)
			apply(t, db, runStmts)

			schemaStmts, err := c.statements(d, collapsed)
			must.NoError(t, err)
			apply(t, db, schemaStmts)

			want := shapeOf(t, db, d, fromRun)
			got := shapeOf(t, db, d, collapsed)

			test.Eq(t, want.tables, got.tables, test.Sprintf("tables"))
			test.Eq(t, want.columns, got.columns, test.Sprintf("columns"))
			test.Eq(t, want.indexes, got.indexes, test.Sprintf("indexes"))

			// A database at the run's latest version takes the schema and is
			// unchanged by it.
			overRun, err := c.statements(d, fromRun)
			must.NoError(t, err)
			apply(t, db, overRun)

			test.Eq(t, want, shapeOf(t, db, d, fromRun), test.Sprintf("the schema changed a table at the run's latest version"))
		})
	}
}

// clientConfig is the minimum database.ClientConfig a client needs.
type clientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*clientConfig)(nil)

func (c *clientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *clientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *clientConfig) GetMaxPingAttempts() uint64        { return 30 }
func (c *clientConfig) GetPingWaitPeriod() time.Duration  { return time.Second }
func (c *clientConfig) GetMaxIdleConns() int              { return 1 }
func (c *clientConfig) GetMaxOpenConns() int              { return 1 }
func (c *clientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// SQLite is in-process, so it runs with every `go test` — and it is the
// dialect whose run did the most, rebuilding the refresh token table rather
// than altering it.
func TestCollapsedRuns_SQLite(T *testing.T) {
	T.Parallel()

	client, err := sqlite.NewDatabaseClient(T.Context(),
		&clientConfig{connectionString: filepath.Join(T.TempDir(), "collapsed.db")})
	must.NoError(T, err)
	T.Cleanup(func() { _ = client.Close() })

	runCollapses(T, client.Writer(), dialect.SQLite)
}

func TestCollapsedRuns_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(_ context.Context, pg *pgtest.Instance) {
		runCollapses(T, pg.DB, dialect.Postgres)
	})
}

func TestCollapsedRuns_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(_ context.Context, my *mysqltest.Instance) {
		runCollapses(T, my.DB, dialect.MySQL)
	}, mysqltest.WithCredentials("collapsed", "collapsed", "collapsed"))
}
