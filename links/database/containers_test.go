package database

import (
	"context"
	stderrors "errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/links"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/mysql"
	"github.com/primandproper/primitives-go/database/postgres"
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
// the server they were written for — and, above all, whether Resolve's
// transaction really hands one link to exactly one caller when the contenders
// are separate connections rather than one serialized file. That last one is
// the guarantee this store exists to make without a lock service, so it is the
// case that has to run on a server.
func runDialectSuite(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	ctx := t.Context()

	waitForServer(t, ctx, client.Writer())
	createTable(t, client, d, DefaultTablePrefix)

	c := newFakeClock()

	store, err := New(&Config{}, client, WithClock(c))
	must.NoError(t, err)

	t.Run("round-trips a record through the server's own column types", func(t *testing.T) {
		record := activeRecord()
		record.Metadata = map[string]string{"next": "/dashboard"}
		put(t, store, "roundtrip", record)

		read, readErr := store.Get(ctx, "roundtrip")
		must.NoError(t, readErr)

		test.EqOp(t, testAction, read.Action)
		test.EqOp(t, testSubject, read.Subject)
		test.EqOp(t, links.StateActive, read.State)
		test.EqOp(t, "/dashboard", read.Metadata["next"])
		test.True(t, read.ExpiresAt.Equal(record.ExpiresAt),
			test.Sprintf("wrote %v, read %v", record.ExpiresAt, read.ExpiresAt))
		test.True(t, read.PurgeAfter.Equal(record.PurgeAfter))
		test.Nil(t, read.ResolvedAt)
	})

	// The whole reason this store needs no locker. On SQLite every writer is
	// serialized by the file, so the case proves nothing there; here the
	// contenders are real connections to a real server, and only the guarded
	// UPDATE's affected-row count stops two of them redeeming one link.
	t.Run("hands one link to exactly one of several concurrent callers", func(t *testing.T) {
		const contenders = 8

		put(t, store, "contended", activeRecord())

		at := mintedAt.Add(time.Minute)

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

				if _, resolveErr := store.Resolve(ctx, "contended",
					links.StateRedeemed, at, at.Add(time.Hour)); resolveErr == nil {
					winners.Add(1)
				}
			}()
		}

		start.Done()
		done.Wait()

		test.EqOp(t, int64(1), winners.Load())
	})

	// The plural revoke and a redemption are two guarded UPDATEs contending for
	// one row, and the revoke runs outside a transaction where the redemption
	// runs inside one — so this is the case where "one statement is already one
	// transaction" is either true or a bug. Only a real server decides it:
	// SQLite serializes every writer through the file.
	//
	// It uses a subject of its own, because a plural revoke is the one write in
	// this suite that reaches rows it was not handed the names of, and the
	// sibling subtests' rows are all minted for testSubject.
	t.Run("does not let a redeemer and a subject-wide revoker both win", func(t *testing.T) {
		const (
			outstanding                = 6
			racedSubject links.Subject = "user_raced"
		)

		ids := make([]links.ID, 0, outstanding)

		for i := range outstanding {
			id := links.ID(fmt.Sprintf("raced_%d", i))

			record := activeRecord()
			record.Subject = racedSubject
			put(t, store, id, record)

			ids = append(ids, id)
		}

		at := mintedAt.Add(time.Minute)

		var (
			start sync.WaitGroup
			done  sync.WaitGroup

			redeemErr error
			revoked   int64
			revokeErr error
		)

		start.Add(1)
		done.Add(2)

		go func() {
			defer done.Done()

			start.Wait()

			_, redeemErr = store.Resolve(ctx, ids[0], links.StateRedeemed, at, at.Add(time.Hour))
		}()

		go func() {
			defer done.Done()

			start.Wait()

			revoked, revokeErr = store.RevokeForSubject(ctx, racedSubject, at, at.Add(time.Hour))
		}()

		start.Done()
		done.Wait()

		must.NoError(t, revokeErr)

		// The assertion, stated as the biconditional it is: the redeemer
		// succeeded if and only if the revoke found one fewer row than the
		// subject had outstanding. Both winning would show up as a redemption
		// that succeeded alongside a revoke that moved all six.
		test.EqOp(t, redeemErr == nil, revoked == outstanding-1,
			test.Sprintf("redeem err %v, revoked %d of %d", redeemErr, revoked, outstanding))

		contended, getErr := store.Get(ctx, ids[0])
		must.NoError(t, getErr)

		if redeemErr == nil {
			test.EqOp(t, links.StateRedeemed, contended.State)
		} else {
			// The row itself is unambiguous, and this read is outside any
			// transaction, so it is the assertion worth making strictly.
			test.EqOp(t, links.StateRevoked, contended.State)

			// The sentence the loser was handed is engine-dependent, and not
			// because of anything this statement does. Resolve re-reads the row
			// inside its own transaction to say which resolution won; under
			// Postgres's READ COMMITTED that re-read sees the revocation and
			// reports ErrLinkRevoked, while under InnoDB's REPEATABLE READ it
			// returns the snapshot the transaction opened with — resolved_at
			// still NULL — so Resolve reaches the last-resort answer its own
			// comment describes. Both are refusals and neither honors the link;
			// the same split applies to a single-link Revoke racing a Redeem,
			// and predates this statement.
			test.True(t,
				stderrors.Is(redeemErr, links.ErrLinkRevoked) ||
					stderrors.Is(redeemErr, links.ErrLinkAlreadyRedeemed),
				test.Sprintf("loser was told %v", redeemErr))
		}

		// Whichever way the contended row went, every other link the subject
		// held is withdrawn: a revoke that lost one row still did its job.
		for _, id := range ids[1:] {
			stored, storedErr := store.Get(ctx, id)
			must.NoError(t, storedErr, must.Sprintf("id %q", id))

			test.EqOp(t, links.StateRevoked, stored.State, test.Sprintf("id %q", id))
		}
	})

	// MySQL reports rows changed rather than matched, so a guarded UPDATE that
	// could write the values a row already held would report zero there and
	// nowhere else. This one always moves resolved_at off NULL; the loser's
	// re-read is what turns the zero into a sentence.
	t.Run("tells the loser which resolution won", func(t *testing.T) {
		put(t, store, "loser", activeRecord())

		at := mintedAt.Add(time.Minute)

		_, resolveErr := store.Resolve(ctx, "loser", links.StateRevoked, at, at.Add(time.Hour))
		must.NoError(t, resolveErr)

		_, resolveErr = store.Resolve(ctx, "loser", links.StateRedeemed, at, at.Add(time.Hour))
		test.ErrorIs(t, resolveErr, links.ErrLinkRevoked)
	})

	// The primary key, which only a real engine enforces the way this schema
	// says it does.
	t.Run("refuses a second row under one digest", func(t *testing.T) {
		put(t, store, "collision", activeRecord())

		test.Error(t, store.Put(ctx, "collision", activeRecord()))
	})

	// The comparison the sweeper turns on. It is a real temporal comparison
	// here and a string comparison on SQLite, so a server run is the only place
	// the former is checked.
	t.Run("sweeps only what is past its purge deadline", func(t *testing.T) {
		short := activeRecord()
		short.PurgeAfter = mintedAt.Add(time.Minute)
		put(t, store, "sweep_short", short)

		long := activeRecord()
		long.PurgeAfter = mintedAt.Add(72 * time.Hour)
		put(t, store, "sweep_long", long)

		c.advance(2 * time.Hour)

		swept, sweepErr := store.Sweep(ctx)
		must.NoError(t, sweepErr)
		test.True(t, swept >= 1, test.Sprintf("swept %d", swept))

		_, getErr := store.Get(ctx, "sweep_short")
		test.ErrorIs(t, getErr, links.ErrLinkNotFound)

		_, getErr = store.Get(ctx, "sweep_long")
		test.NoError(t, getErr)

		c.advance(-2 * time.Hour)
	})

	// A prefix is not decoration: it renders a second table, and both the DDL
	// and every statement have to agree about which one they mean.
	t.Run("serves a namespaced table alongside the plain one", func(t *testing.T) {
		createTable(t, client, d, "ddb")

		namespaced, storeErr := New(&Config{TablePrefix: "ddb"}, client, WithClock(c))
		must.NoError(t, storeErr)

		put(t, namespaced, "namespaced", activeRecord())

		// The plain store cannot see it, which is what a namespace is for.
		_, getErr := store.Get(ctx, "namespaced")
		test.ErrorIs(t, getErr, links.ErrLinkNotFound)

		_, getErr = namespaced.Get(ctx, "namespaced")
		test.NoError(t, getErr)
	})

	// A row written by another shape of links.Record reads as absent rather
	// than being decoded with the wrong field meanings, and the version is a
	// column rather than part of the metadata blob so that the engine's own
	// integer decides it.
	t.Run("reports a row written by another version as stale", func(t *testing.T) {
		record := activeRecord()
		record.Version = links.RecordVersion + 1
		put(t, store, "stale", record)

		_, getErr := store.Get(ctx, "stale")
		test.ErrorIs(t, getErr, links.ErrStaleRecord)
		test.False(t, stderrors.Is(getErr, links.ErrLinkNotFound))
	})
}

func TestLinks_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx,
			&testClientConfig{connectionString: pg.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.Postgres)
	})
}

func TestLinks_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx,
			&testClientConfig{connectionString: my.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("linkstest", "linkstest", "linkstest"))
}
