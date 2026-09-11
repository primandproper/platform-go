package identity

import (
	"context"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// defaultMySQLImage pins the MariaDB flavor this suite exercises; mysqltest's
// default is stock MySQL.
const defaultMySQLImage = "mariadb:11"

// TestSQLStore_RealServers runs the same behavioral suite SQLite runs, against
// real servers.
//
// It exists because the SQL that only a real server can validate is otherwise
// merely rendered, never executed: numbered versus positional placeholders, ON
// CONFLICT versus ON DUPLICATE KEY on the membership upsert, the LIKE ESCAPE
// clause under MySQL's NO_BACKSLASH_ESCAPES-sensitive default, the roster join's
// twenty-eight column projection, native boolean and timestamp handling across
// three drivers, and the partial indexes that only two of the three dialects
// have — where MySQL's whole-table equivalents must still serve the same
// queries.
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
			// runs once — the tables carry IF NOT EXISTS, the indexes cannot.
			// The index key lengths are what this is really checking: scope plus
			// a 320-character email address has to stay inside InnoDB's limit,
			// and it fails here rather than in a consumer's migration if it
			// does not.
			for _, stmt := range stmts {
				_, execErr := client.Writer().ExecContext(ctx, stmt)
				must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
			}
		})
	})

	T.Run("statements carry no unrendered placeholder", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := migrations.Statements(d, "ddl_check")
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.False(t, strings.Contains(stmt, "{{"))
			}
		}
	})
}

// TestUniqueness_RealServers proves the unique indexes are what actually
// guarantee uniqueness, rather than the read SQLStore runs first.
//
// The pre-check turns the ordinary collision into ErrUsernameTaken; two
// registrations racing for one handle reach the index instead, and the loser
// must be refused by the server. Only a real server enforces that — SQLite in
// this suite runs one connection at a time.
func TestUniqueness_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(_ context.Context, pg *pgtest.Instance) {
			client, err := postgres.NewDatabaseClient(t.Context(),
				&testClientConfig{connectionString: pg.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			assertIndexRefusesDuplicate(t, &storeEnv{client: client, dialect: dialect.Postgres})
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(_ context.Context, client database.Client) {
			assertIndexRefusesDuplicate(t, &storeEnv{client: client, dialect: dialect.MySQL})
		})
	})
}

// assertIndexRefusesDuplicate writes a colliding row past the pre-check, by
// issuing the INSERT the store would issue directly.
func assertIndexRefusesDuplicate(t *testing.T, env *storeEnv) {
	t.Helper()

	store := env.newStore(t)
	seedUser(t, env, store, newUser("ada"))

	duplicate := newUser("ada")
	duplicate.EmailAddress = "someone-else@example.com"
	duplicate.ID = identifiers.New()

	err := store.q.CreateUser(t.Context(), env.client.Writer(), createUserParams(duplicate))
	must.Error(t, err)
}

// runWithMySQL starts a MySQL-flavored container and hands the closure a
// database.Client against it.
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
