package database

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/mysql"
	"github.com/primandproper/platform-go/v13/database/postgres"
	"github.com/primandproper/platform-go/v13/sessions"
	"github.com/primandproper/platform-go/v13/testutils/containers/mysqltest"
	"github.com/primandproper/platform-go/v13/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// waitForServer polls until the server accepts a trivial statement.
//
// A container's readiness log precedes it actually accepting connections —
// MySQL's entrypoint in particular logs "ready for connections" and then
// restarts — and NewDatabaseClient does not ping on construction. Without this
// the first DDL statement lands on a socket that is still closing and fails
// with an unhelpful "invalid connection".
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

// runDialectSuite is the behavioral suite the SQLite tests already run,
// executed against a real server.
//
// SQLite covers the logic; what only a real server can validate is whether the
// DDL, the numbered placeholders, the dialect-specific insert-ignore clause,
// and the temporal column types are actually accepted — and, above all, whether
// the round trip through a real DATETIME(6) or TIMESTAMPTZ returns the same
// instant that went in. It does not on nanosecond input, which is why the store
// truncates.
func runDialectSuite(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	ctx := t.Context()

	waitForServer(t, ctx, client.Writer())
	createTable(t, client, d, DefaultTablePrefix)

	c := newFakeClock()

	backend, err := NewBackend[principal](&Config{}, client, WithClock(c))
	must.NoError(t, err)

	t.Run("round-trips a record through the server's own column types", func(t *testing.T) {
		want := testRecord(c, "u_1")
		must.NoError(t, backend.Create(ctx, "round-trip", want, time.Hour))

		got, loadErr := backend.Load(ctx, "round-trip")
		must.NoError(t, loadErr)

		// Equal instants, not merely close ones: the store's whole timeout
		// model compares these against a clock, and a lossy round trip would
		// shift every deadline by up to a second.
		test.True(t, want.CreatedAt.Equal(got.CreatedAt),
			test.Sprintf("wrote %v, read %v", want.CreatedAt, got.CreatedAt))
		test.True(t, want.LastSeenAt.Equal(got.LastSeenAt),
			test.Sprintf("wrote %v, read %v", want.LastSeenAt, got.LastSeenAt))
		test.EqOp(t, "u_1", got.Data.UserID)
	})

	t.Run("reports a duplicate identifier rather than raising", func(t *testing.T) {
		must.NoError(t, backend.Create(ctx, "duplicate", testRecord(c, "u_1"), time.Hour))

		test.ErrorIs(t,
			backend.Create(ctx, "duplicate", testRecord(c, "u_2"), time.Hour),
			sessions.ErrIDConflict)
	})

	t.Run("refuses to resurrect a deleted row", func(t *testing.T) {
		must.NoError(t, backend.Create(ctx, "signed-out", testRecord(c, "u_1"), time.Hour))
		must.NoError(t, backend.Delete(ctx, "signed-out"))

		test.ErrorIs(t,
			backend.Update(ctx, "signed-out", testRecord(c, "u_1"), time.Hour),
			sessions.ErrNotFound)
	})

	// MySQL reports zero rows affected for an update that changed nothing, so
	// this is the case the existence fallback exists for — and the one only a
	// real MySQL can exercise.
	t.Run("an identical rewrite is not a missing session", func(t *testing.T) {
		record := testRecord(c, "u_1")

		must.NoError(t, backend.Create(ctx, "rewritten", record, time.Hour))
		must.NoError(t, backend.Update(ctx, "rewritten", record, time.Hour))
		must.NoError(t, backend.Update(ctx, "rewritten", record, time.Hour))
	})

	t.Run("renews inside one transaction", func(t *testing.T) {
		must.NoError(t, backend.Create(ctx, "renew-old", testRecord(c, "u_1"), time.Hour))
		must.NoError(t, backend.Rename(ctx, "renew-old", "renew-new", testRecord(c, "u_1"), time.Hour))

		_, loadErr := backend.Load(ctx, "renew-old")
		test.ErrorIs(t, loadErr, sessions.ErrNotFound)

		_, loadErr = backend.Load(ctx, "renew-new")
		must.NoError(t, loadErr)
	})

	t.Run("rolls a renewal back when the new identifier is taken", func(t *testing.T) {
		must.NoError(t, backend.Create(ctx, "rollback-old", testRecord(c, "u_1"), time.Hour))
		must.NoError(t, backend.Create(ctx, "rollback-taken", testRecord(c, "u_2"), time.Hour))

		test.ErrorIs(t,
			backend.Rename(ctx, "rollback-old", "rollback-taken", testRecord(c, "u_1"), time.Hour),
			sessions.ErrIDConflict)

		// Both survive: a failed renewal leaves the old identifier resolving
		// rather than leaving the user with neither.
		_, loadErr := backend.Load(ctx, "rollback-old")
		must.NoError(t, loadErr)

		_, loadErr = backend.Load(ctx, "rollback-taken")
		must.NoError(t, loadErr)
	})

	// The comparison the sweeper turns on. It is a real temporal comparison
	// here and a string comparison on SQLite, so a server run is the only place
	// the former is checked.
	t.Run("sweeps only what is past its deadline", func(t *testing.T) {
		must.NoError(t, backend.Create(ctx, "sweep-short", testRecord(c, "u_1"), time.Minute))
		must.NoError(t, backend.Create(ctx, "sweep-long", testRecord(c, "u_2"), 48*time.Hour))

		c.advance(2 * time.Hour)

		_, sweepErr := backend.Sweep(ctx)
		must.NoError(t, sweepErr)

		_, loadErr := backend.Load(ctx, "sweep-short")
		test.ErrorIs(t, loadErr, sessions.ErrNotFound)

		_, loadErr = backend.Load(ctx, "sweep-long")
		must.NoError(t, loadErr)

		c.advance(-2 * time.Hour)
	})

	t.Run("serves a whole session lifecycle under a store", func(t *testing.T) {
		store, storeErr := sessions.NewStore(backend, sessions.WithClock(c), sessions.WithIdleTimeout(10*time.Minute))
		must.NoError(t, storeErr)

		session, storeErr := store.New(ctx, &principal{UserID: "u_1", Admin: true})
		must.NoError(t, storeErr)

		read, storeErr := store.Get(ctx, session.ID)
		must.NoError(t, storeErr)
		test.True(t, read.Data.Admin)

		must.NoError(t, store.Save(ctx, session.ID, &principal{UserID: "u_1"}))

		renewed, storeErr := store.Renew(ctx, session.ID)
		must.NoError(t, storeErr)

		read, storeErr = store.Get(ctx, renewed)
		must.NoError(t, storeErr)
		test.False(t, read.Data.Admin)
		test.True(t, session.CreatedAt.Equal(read.CreatedAt))

		must.NoError(t, store.Delete(ctx, renewed))

		_, storeErr = store.Get(ctx, renewed)
		test.ErrorIs(t, storeErr, sessions.ErrNotFound)
	})
}

func TestSessionsDatabase_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.Postgres)
	})
}

func TestSessionsDatabase_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("sessionstest", "sessionstest", "sessionstest"))
}
