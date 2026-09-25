package recoverycodes

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/tenancy"
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

// runDialectSuite is what only a real server can decide.
//
// SQLite covers the logic; what it cannot cover is whether the DDL, the
// placeholders and the engine's own temporal types are accepted by the server
// they were written for — and, above all, whether a spend really hands one code
// to exactly one caller when the contenders hold separate transactions on
// separate connections rather than one serialized file.
//
// Every subtest keys on a user of its own, because they share one table: what is
// asserted is which rows survive, never how many the table holds.
func runDialectSuite(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	waitForServer(t, t.Context(), client.Writer())
	createTable(t, client, d, DefaultTablePrefix)

	h := newHarnessOn(t, client, &Config{})

	t.Run("round-trips a set through the server's own column types", func(t *testing.T) {
		codes := h.mint(t, "user_roundtrip")

		must.NoError(t, h.consume(t, testScope(), "user_roundtrip", codes[0]))
		test.EqOp(t, testCount-1, h.remaining(t, testScope(), "user_roundtrip"))

		records, err := h.store.ListForUser(t.Context(), client.Reader(), testScope(), "user_roundtrip")
		must.NoError(t, err)
		must.SliceLen(t, testCount, records)

		for _, record := range records {
			test.True(t, record.IssuedAt.Equal(h.clock.Now()),
				test.Sprintf("issued %v, read %v", h.clock.Now(), record.IssuedAt))
		}
	})

	// The whole reason the spend is a guarded update. On SQLite every writer is
	// serialized by the file, so the case proves nothing there; here each
	// contender checks the code and then spends it in its own transaction on its
	// own connection, which is exactly the shape signin's doors have — and only
	// the affected-row count stops two of them both going through.
	t.Run("hands one code to exactly one of several concurrent callers", func(t *testing.T) {
		const contenders = 8

		code := h.mint(t, "user_race")[0]

		var (
			start    sync.WaitGroup
			done     sync.WaitGroup
			verified atomic.Int64
			winners  atomic.Int64
		)

		start.Add(1)
		done.Add(contenders)

		for range contenders {
			go func() {
				defer done.Done()

				start.Wait()

				if h.store.Verify(t.Context(), client.Reader(), testScope(), "user_race", code) != nil {
					return
				}

				verified.Add(1)

				if h.consume(t, testScope(), "user_race", code) == nil {
					winners.Add(1)
				}
			}()
		}

		start.Done()
		done.Wait()

		test.EqOp(t, int64(1), winners.Load())
		test.True(t, verified.Load() >= 1)
		test.EqOp(t, testCount-1, h.remaining(t, testScope(), "user_race"))
	})

	// The composite key, which only a real engine enforces the way this schema
	// says it does: two people holding the same code is two rows, and one person
	// shown the same code twice is a failed replacement.
	t.Run("keys a code on its owner as well as its digest", func(t *testing.T) {
		repeating := newHarnessOn(t, client, &Config{},
			WithGenerator(&constantGenerator{raw: []byte("abcdefgh")}))

		_, err := repeating.replace(t, testScope(), "user_collision_a", 1)
		must.NoError(t, err)

		_, err = repeating.replace(t, testScope(), "user_collision_b", 1)
		must.NoError(t, err)

		_, err = repeating.replace(t, testScope(), "user_collision_a", 2)
		test.Error(t, err)

		test.EqOp(t, 1, h.remaining(t, testScope(), "user_collision_a"))
	})

	// The scope predicate, against a server that would happily return another
	// tenant's row if the statement let it.
	t.Run("keeps one tenant's codes out of another's reach", func(t *testing.T) {
		codes, err := h.replace(t, tenancy.Of("tenant_scoped"), "user_scoped", testCount)
		must.NoError(t, err)

		test.ErrorIs(t, h.consume(t, tenancy.Of("tenant_other"), "user_scoped", codes[0]), signin.ErrInvalidCredentials)
		test.NoError(t, h.consume(t, tenancy.Of("tenant_scoped"), "user_scoped", codes[0]))
	})

	t.Run("deletes one person's set and leaves their neighbour's", func(t *testing.T) {
		h.mint(t, "user_erased")
		theirs := h.mint(t, "user_neighbor")

		deleted, err := h.deleteForUser(t, testScope(), "user_erased")
		must.NoError(t, err)
		test.EqOp(t, int64(testCount), deleted)

		test.NoError(t, h.consume(t, testScope(), "user_neighbor", theirs[0]))
	})

	// A prefix is not decoration: it renders a second table, and both the DDL and
	// every statement have to agree about which one they mean.
	t.Run("serves a namespaced table alongside the plain one", func(t *testing.T) {
		createTable(t, client, d, "ddb")

		namespaced := newHarnessOn(t, client, &Config{TablePrefix: "ddb"})
		codes := namespaced.mint(t, "user_namespaced")

		test.ErrorIs(t, h.consume(t, testScope(), "user_namespaced", codes[0]), signin.ErrInvalidCredentials)
		test.NoError(t, namespaced.consume(t, testScope(), "user_namespaced", codes[0]))
	})
}

func TestRecoveryCodes_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.Postgres)
	})
}

func TestRecoveryCodes_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("recoverycodetest", "recoverycodetest", "recoverycodetest"))
}
