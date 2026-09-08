package billing

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/billing/migrations"

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
// It exists because the SQL that only a real server can validate is otherwise
// merely rendered, never executed: numbered placeholders, the partial indexes
// Postgres and SQLite have and MySQL does not, and — the two this catches — a
// unique index over a nullable column, and a paid period compared against a real
// temporal type rather than against text.
//
// The nullable-uniqueness one is the reason this suite is not optional. This
// schema leans on all three engines admitting more than one NULL in a unique
// index, which is what lets a free tier and a comped plan coexist. SQLite agrees
// and proves nothing about the other two.
func TestSQLStore_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(_ context.Context, pg *pgtest.Instance) {
			client, err := postgres.NewDatabaseClient(t.Context(),
				&testClientConfig{connectionString: pg.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			runStoreSuite(t, &storeEnv{
				client:           client,
				dialect:          dialect.Postgres,
				connectionString: pg.ConnectionString,
			})
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(_ context.Context, client database.Client, connectionString string) {
			runStoreSuite(t, &storeEnv{
				client:           client,
				dialect:          dialect.MySQL,
				connectionString: connectionString,
			})
		})
	})
}

// TestMigrations_RealServers proves the shipped DDL is accepted verbatim by each
// server, independent of whether the store then exercises every column.
//
// MySQL is the one with something to prove here beyond parsing: its unique keys
// span a scope and a provider identifier, and InnoDB bounds a key at 3072 bytes —
// so a column widened without counting them is a CREATE that fails on the server
// and nowhere else. Its foreign keys are also declared inline rather than as
// column references, which is a different code path in the renderer.
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

		runWithMySQL(t, func(ctx context.Context, client database.Client, _ string) {
			stmts, err := migrations.Statements(dialect.MySQL, "ddl_check")
			must.NoError(t, err)

			// MySQL has no CREATE INDEX IF NOT EXISTS, so unlike Postgres this
			// runs once — the tables carry IF NOT EXISTS, the indexes cannot.
			for _, stmt := range stmts {
				_, execErr := client.Writer().ExecContext(ctx, stmt)
				must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
			}
		})
	})
}

// runWithMySQL starts a MySQL-flavored container and hands the closure a
// database.Client against it.
func runWithMySQL(t *testing.T, fn func(ctx context.Context, client database.Client, connectionString string)) {
	t.Helper()

	mysqltest.Run(t, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx,
			&testClientConfig{connectionString: my.ConnectionString})
		must.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })

		fn(ctx, client, my.ConnectionString)
	}, mysqltest.WithImage(defaultMySQLImage))
}
