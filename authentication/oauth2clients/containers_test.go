package oauth2clients

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test/must"
)

// defaultMySQLImage pins the MariaDB flavor this suite exercises; mysqltest's
// default is stock MySQL.
const defaultMySQLImage = "mariadb:11"

// TestSQLStore_RealServers runs the same behavioral suite SQLite runs, against
// real servers.
//
// It exists because the parts of these statements only a real server can
// validate are otherwise merely rendered. Three of them matter here. The unique
// index on client_id is what makes the authorization server's scopeless lookup a
// total function, and it is the one constraint this schema relies on for
// correctness rather than for tidiness. The insert's conflict clause is spelled
// two ways — ON CONFLICT DO NOTHING on Postgres and SQLite, INSERT IGNORE on
// MySQL — and "zero rows means the identifier is taken" has to mean the same
// thing under both. And the two paged lists' count subqueries take arguments
// each analyzer types differently.
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
// rather than matched, and all three counted writes in this store assign
// something a matched row cannot already hold: the update sets
// last_updated_at = CURRENT_TIMESTAMP(6), the archive sets archived_at on a row
// the statement requires to be unarchived, and the create is an insert whose
// zero is a conflict rather than a no-change. A future write that assigned only
// values a row may already hold would need clientFoundRows=true in the DSN, or a
// predicate that discriminates.
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
