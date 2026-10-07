package devices

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// waitForServer polls until the server accepts a trivial statement.
//
// A container's readiness log precedes it actually accepting connections, and
// NewDatabaseClient does not ping on construction — so without this the first
// DDL statement can land on a socket that is still closing.
func waitForServer(tb testing.TB, ctx context.Context, q database.SQLQueryExecutor) {
	tb.Helper()

	var lastErr error
	for range 30 {
		if _, err := q.ExecContext(ctx, "SELECT 1"); err == nil {
			return
		} else { //nolint:revive // the error is only reported if every attempt fails
			lastErr = err
		}

		time.Sleep(time.Second)
	}

	tb.Fatalf("database never accepted a statement: %v", lastErr)
}

// runDialectSuite is what only a real server can decide: whether the DDL, the
// upsert's two spellings of a conflict branch, the expanded set and the engine's
// own temporal types are accepted by the server they were written for.
//
// It runs the suite SQLite runs, then the sweep, which spans every scope and so
// asserts on which rows survive rather than on how many it removed — the table
// is shared with every subtest before it.
func runDialectSuite(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	waitForServer(t, t.Context(), client.Writer())
	createTable(t, client, d, DefaultTablePrefix)

	h := newHarnessOn(t, client, &Config{})

	runStoreSuite(t, h)

	t.Run("sweeps a lapsed login and keeps a live one", func(t *testing.T) {
		const user = "user_swept"

		h.mustRecord(t, testScope(), &Sighting{FamilyID: "family_swept_lapsed", UserID: user, ExpiresAt: h.clock.Now().Add(time.Minute)})
		h.mustRecord(t, testScope(), &Sighting{FamilyID: "family_swept_live", UserID: user, ExpiresAt: h.clock.Now().Add(testTTL)})

		h.clock.advance(time.Hour)

		_, err := h.store.Sweep(t.Context())
		must.NoError(t, err)

		remaining := h.listForUser(t, testScope(), user)
		must.SliceLen(t, 1, remaining)
		test.EqOp(t, "family_swept_live", remaining[0].FamilyID)
	})

	// A prefix is not decoration: it renders a second table, and both the DDL and
	// every statement have to agree about which one they mean.
	t.Run("serves a namespaced table alongside the plain one", func(t *testing.T) {
		createTable(t, client, d, "app")

		namespaced := newHarnessOn(t, client, &Config{TablePrefix: "app"})
		namespaced.mustRecord(t, testScope(), namespaced.sighting("family_namespaced", "user_namespaced", browser))

		test.SliceEmpty(t, h.listForUser(t, testScope(), "user_namespaced"))
		test.SliceLen(t, 1, namespaced.listForUser(t, testScope(), "user_namespaced"))
	})
}

func TestDevices_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.Postgres)
	})
}

func TestDevices_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("signindevicetest", "signindevicetest", "signindevicetest"))
}
