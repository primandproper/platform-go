package grants

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/grants/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test/must"
)

// TestSQLStore_RealServers runs the same behavioral suite SQLite runs, against
// real servers: numbered versus positional placeholders, three drivers' handling
// of the bytes columns, and — the one this table turns on — a compare-and-set
// whose predicate compares a BLOB.
func TestSQLStore_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		runWithPostgres(t, func(_ context.Context, client database.Client) {
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

// TestMigrations_RealServers proves the shipped DDL is accepted verbatim, twice,
// by each server — and on MySQL that the three-column unique key fits InnoDB's
// key length.
func TestMigrations_RealServers(T *testing.T) {
	T.Parallel()

	for name, run := range map[string]func(*testing.T, func(context.Context, database.Client)){
		string(dialect.Postgres): runWithPostgres,
		string(dialect.MySQL):    runWithMySQL,
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			run(t, func(ctx context.Context, client database.Client) {
				stmts, err := migrations.Statements(dialect.Dialect(name), "ddl_check")
				must.NoError(t, err)

				for range 2 {
					for _, stmt := range stmts {
						_, execErr := client.Writer().ExecContext(ctx, stmt)
						must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
					}
				}
			})
		})
	}
}

// realServerConns is the pool a real server's client gets: enough for a
// transaction the suite holds open and a second writer racing it.
const realServerConns = 4

func runWithPostgres(t *testing.T, fn func(ctx context.Context, client database.Client)) {
	t.Helper()

	pgtest.Run(t, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString, maxOpenConns: realServerConns})
		must.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })

		fn(ctx, client)
	})
}

func runWithMySQL(t *testing.T, fn func(ctx context.Context, client database.Client)) {
	t.Helper()

	mysqltest.Run(t, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString, maxOpenConns: realServerConns})
		must.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })

		fn(ctx, client)
	})
}
