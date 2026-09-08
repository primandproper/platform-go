package comments

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/comments/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/mysql"
	"github.com/primandproper/primitives-go/database/postgres"
	"github.com/primandproper/primitives-go/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/testutils/containers/pgtest"

	"github.com/shoenig/test/must"
)

// defaultMySQLImage pins the MariaDB flavor this suite exercises; mysqltest's
// default is stock MySQL.
const defaultMySQLImage = "mariadb:11"

// TestSQLStore_RealServers runs the same behavioral suite SQLite runs, against
// real servers.
//
// It exists because the parts of these statements that only a real server can
// validate are otherwise merely rendered. The empty-string parent is the one
// this catches: the discussion's two reads are one statement whose parent is a
// bound value, and an engine that treated ” as anything but a value — or a
// driver that bound it as NULL — would answer the roots read with nothing at
// all. Beside it are the numbered placeholders, the partial indexes Postgres and
// SQLite have and MySQL does not, and the three paged lists' count subqueries,
// whose arguments each analyzer types differently.
func TestSQLStore_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(_ context.Context, pg *pgtest.Instance) {
			client, err := postgres.NewDatabaseClient(t.Context(),
				&testClientConfig{connectionString: pg.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			runStoreSuite(t, &storeEnv{client: client, dialect: dialect.Postgres})
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(_ context.Context, client database.Client) {
			runStoreSuite(t, &storeEnv{client: client, dialect: dialect.MySQL})
		})
	})
}

// TestMigrations_RealServers proves the shipped DDL is accepted verbatim by each
// server, independent of whether the store then exercises every column.
func TestMigrations_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(ctx context.Context, pg *pgtest.Instance) {
			stmts, err := migrations.Statements(dialect.Postgres, "ddl_check")
			must.NoError(t, err)

			// Executed twice: every statement is IF NOT EXISTS, so re-running a
			// migration must be a no-op rather than an error.
			for range 2 {
				for _, stmt := range stmts {
					_, execErr := pg.DB.ExecContext(ctx, stmt)
					must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
				}
			}
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(ctx context.Context, client database.Client) {
			stmts, err := migrations.Statements(dialect.MySQL, "ddl_check")
			must.NoError(t, err)

			// MySQL has no CREATE INDEX IF NOT EXISTS, so unlike Postgres this
			// runs once — the table carries IF NOT EXISTS, the indexes cannot.
			for _, stmt := range stmts {
				_, execErr := client.Writer().ExecContext(ctx, stmt)
				must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
			}
		})
	})
}

// runWithMySQL starts a MySQL-flavored container and hands the closure a
// database.Client against it.
//
// The DSN is the container's, unmodified, and the :execrows divergence MySQL is
// known for is bounded rather than absent here. MySQL counts rows *changed*
// rather than matched, and both counted writes in this store assign something a
// matched row cannot already hold: the edit sets last_updated_at =
// CURRENT_TIMESTAMP(6), and the archive sets archived_at on a row the statement
// requires to be unarchived. A future write that assigned only values a row may
// already hold would need clientFoundRows=true in the DSN, or a predicate that
// discriminates.
func runWithMySQL(t *testing.T, fn func(ctx context.Context, client database.Client)) {
	t.Helper()

	mysqltest.Run(t, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx,
			&testClientConfig{connectionString: my.ConnectionString})
		must.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })

		fn(ctx, client)
	}, mysqltest.WithImage(defaultMySQLImage))
}
