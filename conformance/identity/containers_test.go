package identity_test

import (
	"context"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test/must"
)

// The environment variables that let this suite run against a server somebody
// else provided.
//
// This is what makes "all three dialects, locally and in CI" one suite rather
// than two. Unset, pgtest starts a container and the developer needs nothing but
// Docker. Set, it connects and starts nothing — so a CI job that already has a
// database, or a developer whose Docker is having a bad day, runs the identical
// assertions against the identical code.
//
// Both have it as of primitives-go v2.7.0. Unset, each starts a container and
// the developer needs nothing but Docker; set, the identical assertions run
// against whatever is on the other end.
const (
	postgresDSNEnv = "CONFORMANCE_POSTGRES_DSN"
	mysqlDSNEnv    = "CONFORMANCE_MYSQL_DSN"
)

// TestConformance_RealServers runs every suite against Postgres and MariaDB.
//
// The assertions are the ones TestConformance_SQLite ran, unchanged and not
// re-stated — which is the whole claim of the seam. What changes is the driver
// underneath, and that is not a formality: a cursor walked over timestamps
// SQLite truncates to the second has a different collision profile on a server
// keeping microseconds, an affected-row count is each driver's own answer, and
// a uniqueness check reads differently under a collation that is not the
// neighbour's. An assertion that passes on SQLite and fails here has found
// something no amount of running it on SQLite would have found.
//
// It gates on RUN_CONTAINER_TESTS through pgtest and mysqltest, and skips
// otherwise — except where a DSN names a server, which starts nothing and so
// has nothing to gate.
func TestConformance_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(_ context.Context, pg *pgtest.Instance) {
			db, err := postgres.NewDatabaseClient(t.Context(),
				&testClientConfig{connectionString: pg.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			runAgainst(t, db, dialect.Postgres)
		}, pgtest.WithDSNFromEnv(postgresDSNEnv))
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		mysqltest.Run(t, func(ctx context.Context, my *mysqltest.Instance) {
			db, err := mysql.NewDatabaseClient(ctx,
				&testClientConfig{connectionString: my.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			runAgainst(t, db, dialect.MySQL)
		}, mysqltest.WithDSNFromEnv(mysqlDSNEnv))
	})
}
