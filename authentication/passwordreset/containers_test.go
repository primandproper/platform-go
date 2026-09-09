package passwordreset

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/mysql"
	"github.com/primandproper/primitives-go/database/postgres"
	"github.com/primandproper/primitives-go/tenancy"
	"github.com/primandproper/primitives-go/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/testutils/containers/pgtest"

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
// numbered placeholders, and the engine's own temporal types are accepted by
// the server they were written for — and, above all, whether a redemption really
// hands one token to exactly one caller when the contenders hold separate
// transactions on separate connections rather than one serialized file.
func runDialectSuite(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	ctx := t.Context()

	waitForServer(t, ctx, client.Writer())
	createTable(t, client, d, DefaultTablePrefix)

	c := newFakeClock()

	store, err := NewSQLStore(&Config{}, client, WithClock(c))
	must.NoError(t, err)

	t.Run("round-trips a token through the server's own column types", func(t *testing.T) {
		issuance, issueErr := issueFor(t, store, testScope(), testUserID, time.Hour)
		must.NoError(t, issueErr)

		token, verifyErr := verify(t, store, testScope(), issuance.Secret)
		must.NoError(t, verifyErr)

		test.EqOp(t, issuance.Token.ID, token.ID)
		test.EqOp(t, testUserID, token.UserID)
		test.EqOp(t, testScope(), token.Scope)
		test.Nil(t, token.RedeemedAt)
		test.True(t, issuance.Token.ExpiresAt.Equal(token.ExpiresAt),
			test.Sprintf("issued %v, read %v", issuance.Token.ExpiresAt, token.ExpiresAt))
	})

	// The whole reason this store enforces single use with a guarded update. On
	// SQLite every writer is serialized by the file, so the case proves nothing
	// there; here each contender opens its own transaction on its own connection
	// to a real server, and only the redemption's affected-row count stops two of
	// them resetting one password.
	//
	// The transactions are the callers' now rather than the store's, which is
	// what makes this the case worth running: the guarantee has to survive being
	// handed out.
	t.Run("hands one token to exactly one of several concurrent consumers", func(t *testing.T) {
		const contenders = 8

		issuance, issueErr := issueFor(t, store, testScope(), testUserID, time.Hour)
		must.NoError(t, issueErr)

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

				if _, consumeErr := consume(t, store, testScope(), issuance.Secret); consumeErr == nil {
					winners.Add(1)
				}
			}()
		}

		start.Done()
		done.Wait()

		test.EqOp(t, int64(1), winners.Load())
	})

	// The unique index, which only a real engine enforces the way this schema
	// says it does.
	t.Run("refuses a second row bearing one digest", func(t *testing.T) {
		repeating, storeErr := NewSQLStore(&Config{}, client,
			WithClock(c), WithGenerator(&constantGenerator{secret: "server-side-collision"}))
		must.NoError(t, storeErr)

		_, issueErr := issueFor(t, repeating, testScope(), testUserID, time.Hour)
		must.NoError(t, issueErr)

		_, issueErr = issueFor(t, repeating, testScope(), testUserID, time.Hour)
		test.Error(t, issueErr)
	})

	// The scope predicate, against a server that would happily return another
	// tenant's row if the statement let it.
	t.Run("keeps one tenant's token out of another's reach", func(t *testing.T) {
		issuance, issueErr := issueFor(t, store, tenancy.Of("tenant_scoped"), testUserID, time.Hour)
		must.NoError(t, issueErr)

		_, verifyErr := verify(t, store, tenancy.Of("tenant_other"), issuance.Secret)
		test.ErrorIs(t, verifyErr, ErrTokenNotFound)

		_, verifyErr = verify(t, store, tenancy.Of("tenant_scoped"), issuance.Secret)
		test.NoError(t, verifyErr)
	})

	// The comparison the sweeper turns on. It is a real temporal comparison here
	// and a string comparison on SQLite, so a server run is the only place the
	// former is checked.
	t.Run("sweeps only what is past its deadline", func(t *testing.T) {
		short, issueErr := issueFor(t, store, testScope(), "sweep_user", time.Minute)
		must.NoError(t, issueErr)

		long, issueErr := issueFor(t, store, testScope(), "sweep_user", 48*time.Hour)
		must.NoError(t, issueErr)

		c.advance(2 * time.Hour)

		swept, sweepErr := store.Sweep(ctx)
		must.NoError(t, sweepErr)
		test.True(t, swept >= 1, test.Sprintf("swept %d", swept))

		_, verifyErr := verify(t, store, testScope(), short.Secret)
		test.ErrorIs(t, verifyErr, ErrTokenNotFound)

		_, verifyErr = verify(t, store, testScope(), long.Secret)
		test.NoError(t, verifyErr)

		c.advance(-2 * time.Hour)
	})

	t.Run("revokes one user's outstanding tokens and nobody else's", func(t *testing.T) {
		mine, issueErr := issueFor(t, store, testScope(), "revoke_user", time.Hour)
		must.NoError(t, issueErr)

		theirs, issueErr := issueFor(t, store, testScope(), "other_user", time.Hour)
		must.NoError(t, issueErr)

		revoked, revokeErr := revokeForUser(t, store, testScope(), "revoke_user")
		must.NoError(t, revokeErr)
		test.EqOp(t, int64(1), revoked)

		_, verifyErr := verify(t, store, testScope(), mine.Secret)
		test.ErrorIs(t, verifyErr, ErrTokenNotFound)

		_, verifyErr = verify(t, store, testScope(), theirs.Secret)
		test.NoError(t, verifyErr)
	})

	// A prefix is not decoration: it renders a second table, and both the DDL
	// and every statement have to agree about which one they mean.
	t.Run("serves a namespaced table alongside the plain one", func(t *testing.T) {
		createTable(t, client, d, "ddb")

		namespaced, storeErr := NewSQLStore(&Config{TablePrefix: "ddb"}, client, WithClock(c))
		must.NoError(t, storeErr)

		issuance, issueErr := issueFor(t, namespaced, testScope(), "namespaced_user", time.Hour)
		must.NoError(t, issueErr)

		// The plain store cannot see it, which is what a namespace is for.
		_, verifyErr := verify(t, store, testScope(), issuance.Secret)
		test.ErrorIs(t, verifyErr, ErrTokenNotFound)

		_, verifyErr = verify(t, namespaced, testScope(), issuance.Secret)
		test.NoError(t, verifyErr)
	})
}

func TestPasswordReset_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.Postgres)
	})
}

func TestPasswordReset_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("resettest", "resettest", "resettest"))
}
