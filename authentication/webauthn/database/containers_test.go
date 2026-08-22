package database

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/mysql"
	"github.com/primandproper/platform-go/v13/database/postgres"
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

// runDialectSuite is what only a real server can decide.
//
// SQLite covers the logic; what it cannot cover is whether the DDL, the
// numbered placeholders, and the dialect's own upsert spelling are accepted by
// the engine they were written for — and, above all, whether Consume's
// transaction really hands one ceremony to exactly one caller when the writers
// are separate connections rather than one serialized file.
func runDialectSuite(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	ctx := t.Context()

	waitForServer(t, ctx, client.Writer())
	createTable(t, client, d, DefaultTablePrefix)

	c := newFakeClock()

	store, err := NewSessionStore(&Config{}, client, WithClock(c))
	must.NoError(t, err)

	t.Run("round-trips ceremony state through the server's own column types", func(t *testing.T) {
		want := testSession("round-trip")
		must.NoError(t, store.Save(ctx, want, time.Hour))

		got, consumeErr := store.Consume(ctx, want.Challenge)
		must.NoError(t, consumeErr)

		test.EqOp(t, want.Challenge, got.Challenge)
		test.Eq(t, want.UserID, got.UserID)
		test.Eq(t, want.AllowedCredentialIDs, got.AllowedCredentialIDs)
		test.True(t, want.Expires.Equal(got.Expires),
			test.Sprintf("saved %v, read %v", want.Expires, got.Expires))
	})

	t.Run("replaces a challenge begun twice", func(t *testing.T) {
		session := testSession("upserted")
		must.NoError(t, store.Save(ctx, session, time.Hour))

		session.UserID = []byte("second-user")
		must.NoError(t, store.Save(ctx, session, time.Hour))

		got, consumeErr := store.Consume(ctx, session.Challenge)
		must.NoError(t, consumeErr)
		test.Eq(t, []byte("second-user"), got.UserID)
	})

	// The whole reason this store exists. On SQLite every writer is serialized
	// by the file, so the case proves nothing there; here the contenders are
	// real connections to a real server, and only the transaction stops two of
	// them completing one ceremony.
	t.Run("hands one ceremony to exactly one of several concurrent consumers", func(t *testing.T) {
		const contenders = 8

		session := testSession("contended")
		must.NoError(t, store.Save(ctx, session, time.Hour))

		var (
			start   sync.WaitGroup
			done    sync.WaitGroup
			winners atomic.Int64
		)

		start.Add(1)
		done.Add(contenders)

		for range contenders {
			go func() {
				defer done.Done()

				start.Wait()

				if _, consumeErr := store.Consume(ctx, session.Challenge); consumeErr == nil {
					winners.Add(1)
				}
			}()
		}

		start.Done()
		done.Wait()

		test.EqOp(t, int64(1), winners.Load())
	})

	// The comparison the sweeper turns on. It is a real temporal comparison
	// here and a string comparison on SQLite, so a server run is the only place
	// the former is checked.
	t.Run("sweeps only what is past its deadline", func(t *testing.T) {
		must.NoError(t, store.Save(ctx, testSession("sweep-short"), time.Minute))
		must.NoError(t, store.Save(ctx, testSession("sweep-long"), 48*time.Hour))

		c.advance(2 * time.Hour)

		swept, sweepErr := store.Sweep(ctx)
		must.NoError(t, sweepErr)
		test.True(t, swept >= 1, test.Sprintf("swept %d", swept))

		_, consumeErr := store.Consume(ctx, "sweep-short")
		test.ErrorIs(t, consumeErr, webauthn.ErrSessionNotFound)

		_, consumeErr = store.Consume(ctx, "sweep-long")
		must.NoError(t, consumeErr)

		c.advance(-2 * time.Hour)
	})

	// A prefix is not decoration: it renders a second table, and both the DDL
	// and every statement have to agree about which one they mean.
	t.Run("serves a namespaced table alongside the plain one", func(t *testing.T) {
		createTable(t, client, d, "ddb")

		namespaced, storeErr := NewSessionStore(&Config{TablePrefix: "ddb"}, client, WithClock(c))
		must.NoError(t, storeErr)

		must.NoError(t, namespaced.Save(ctx, testSession("namespaced"), time.Hour))

		// The plain store cannot see it, which is what a namespace is for.
		_, consumeErr := store.Consume(ctx, "namespaced")
		test.ErrorIs(t, consumeErr, webauthn.ErrSessionNotFound)

		_, consumeErr = namespaced.Consume(ctx, "namespaced")
		must.NoError(t, consumeErr)
	})
}

func TestWebAuthnDatabase_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.Postgres)
	})
}

func TestWebAuthnDatabase_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("webauthntest", "webauthntest", "webauthntest"))
}
