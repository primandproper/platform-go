package database

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/authentication/oauth2server/oauth2servertest"
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
// SQLite covers the logic, and the whole conformance suite already runs against
// it. What it cannot cover is whether the DDL, the numbered placeholders, and
// each dialect's own spelling of "skip a duplicate row" are accepted by the
// engine they were written for — nor, above all, whether the guarded UPDATE
// really resolves two concurrent redemptions to one winner when the contenders
// are separate connections to a real server rather than writers serialized by
// one file.
//
// A dialect branch that renders SQL no engine accepts passes every SQLite test
// in this package and fails every request in production.
func runDialectSuite(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	ctx := t.Context()

	waitForServer(t, ctx, client.Writer())
	createTables(t, client, d, DefaultTablePrefix)

	// The whole contract, against the engine it was written for. Every
	// identifier the suite writes carries a unique suffix, so one database
	// serves every subtest — and nothing declares WithInstanceLocalState,
	// because two Stores over one server are two handles to the same rows.
	t.Run("conformance", func(t *testing.T) {
		oauth2servertest.Run(t, func(tb testing.TB) oauth2server.Store {
			tb.Helper()

			store, err := NewStore(&Config{}, client)
			must.NoError(tb, err)

			// Deliberately not closed: the client is shared by every subtest.
			return store
		})
	})

	store, err := NewStore(&Config{}, client)
	must.NoError(t, err)

	// The insert-ignore clause is spelled differently in each dialect, and it is
	// what turns a duplicate primary key into zero rows affected rather than a
	// SQLSTATE this package would have to parse. Getting it wrong for one engine
	// makes ErrClientExists an unrecognized driver error there and nowhere else.
	t.Run("a duplicate registration is refused rather than raised", func(t *testing.T) {
		client := &oauth2server.Client{
			CreatedAt:    time.Now().UTC().Truncate(time.Microsecond),
			ID:           "duplicate_" + string(d),
			RedirectURIs: []string{"https://client.example/cb"},
		}

		must.NoError(t, store.CreateClient(ctx, client))
		test.ErrorIs(t, store.CreateClient(ctx, client), oauth2server.ErrClientExists)
	})

	// The same clause on the three credential tables, which key on hash rather
	// than on id.
	t.Run("a duplicate credential is refused rather than raised", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)

		code := &oauth2server.AuthorizationCode{
			IssuedAt: now, ExpiresAt: now.Add(time.Hour),
			Hash: oauth2server.Hash("dup_code_" + string(d)), ClientID: "x",
		}
		must.NoError(t, store.CreateAuthorizationCode(ctx, code))
		test.ErrorIs(t, store.CreateAuthorizationCode(ctx, code), oauth2server.ErrRecordExists)

		access := &oauth2server.AccessToken{
			IssuedAt: now, ExpiresAt: now.Add(time.Hour),
			Hash: oauth2server.Hash("dup_access_" + string(d)), ClientID: "x",
		}
		must.NoError(t, store.CreateAccessToken(ctx, access))
		test.ErrorIs(t, store.CreateAccessToken(ctx, access), oauth2server.ErrRecordExists)

		refresh := &oauth2server.RefreshToken{
			IssuedAt: now, ExpiresAt: now.Add(time.Hour),
			Hash: oauth2server.Hash("dup_refresh_" + string(d)), ClientID: "x",
		}
		must.NoError(t, store.CreateRefreshToken(ctx, refresh))
		test.ErrorIs(t, store.CreateRefreshToken(ctx, refresh), oauth2server.ErrRecordExists)
	})

	// NULL is a real absence here rather than a magic date, and the predicate
	// that sweeps registrations has to say so. On SQLite a bound time is stored
	// as Go's own string rendering; on a server it is a temporal type, so this
	// is the only place the comparison is the one the schema describes.
	t.Run("a registration with no expiry survives a sweep", func(t *testing.T) {
		must.NoError(t, store.CreateClient(ctx, &oauth2server.Client{
			CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
			ID:        "eternal_" + string(d),
		}))

		// Well before anything the conformance suite writes for its own sweeps,
		// so this reaches only rows already dead by an hour.
		_, sweepErr := store.Sweep(ctx, time.Now().UTC().Add(-time.Hour))
		must.NoError(t, sweepErr)

		got, readErr := store.GetClient(ctx, "eternal_"+string(d))
		must.NoError(t, readErr)
		test.True(t, got.ExpiresAt.IsZero())
	})

	// A prefix is not decoration: it renders four more tables, and both the DDL
	// and every statement have to agree about which set they mean.
	t.Run("serves a namespaced schema alongside the plain one", func(t *testing.T) {
		createTables(t, client, d, "ddb")

		namespaced, storeErr := NewStore(&Config{TablePrefix: "ddb"}, client)
		must.NoError(t, storeErr)

		must.NoError(t, namespaced.CreateClient(ctx, &oauth2server.Client{
			CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
			ID:        "namespaced",
		}))

		// The plain store cannot see it, which is what a namespace is for.
		_, plainErr := store.GetClient(ctx, "namespaced")
		test.ErrorIs(t, plainErr, oauth2server.ErrNotFound)

		got, readErr := namespaced.GetClient(ctx, "namespaced")
		must.NoError(t, readErr)
		test.EqOp(t, "namespaced", got.ID)
	})
}

func TestOAuth2Database_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx,
			&testClientConfig{connectionString: pg.ConnectionString, maxOpenConns: 16})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.Postgres)
	})
}

func TestOAuth2Database_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx,
			&testClientConfig{connectionString: my.ConnectionString, maxOpenConns: 16})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("oauth2test", "oauth2test", "oauth2test"))
}
