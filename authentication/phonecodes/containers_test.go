package phonecodes

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// waitForServer polls until the server accepts a trivial statement. A
// container's readiness log precedes it actually accepting connections, and
// NewDatabaseClient does not ping on construction.
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

// runDialectSuite is the one suite, over a client of the dialect.
//
// SQLite runs it too, beside the two servers, so the promise that the three
// dialects behave alike is checked by one set of cases rather than argued. What
// only a server can decide is the concurrency: there each contender holds its
// own transaction on its own connection, where SQLite serializes them on the
// file.
//
// Every subtest keys on a number of its own, because they share one table:
// what is asserted is which rows survive, never how many the table holds.
func runDialectSuite(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	ctx := t.Context()

	waitForServer(t, ctx, client.Writer())
	createTable(t, client, d, DefaultTablePrefix)

	c := newFakeClock()

	store, err := NewSQLStore(client, WithClock(c))
	must.NoError(t, err)

	send := func(tb testing.TB, phone string) *Issuance {
		tb.Helper()

		issuance, issueErr := issueFor(tb, store, testScope(), &IssueRequest{SubjectID: "contact_" + phone, PhoneNumber: phone})
		must.NoError(tb, issueErr)

		return issuance
	}

	t.Run("round-trips a code through the server's own column types", func(t *testing.T) {
		issuance := send(t, "+15550000001")

		spent, redeemErr := redeem(t, store, testScope(), "+15550000001", issuance.Plaintext)
		must.NoError(t, redeemErr)

		test.EqOp(t, issuance.Code.ID, spent.ID)
		test.EqOp(t, testScope(), spent.Scope)
		test.EqOp(t, DefaultMaxAttempts, spent.MaxAttempts)
		test.True(t, issuance.Code.ExpiresAt.Truncate(time.Second).Equal(spent.ExpiresAt.Truncate(time.Second)),
			test.Sprintf("issued %v, read %v", issuance.Code.ExpiresAt, spent.ExpiresAt))
		must.NotNil(t, spent.RedeemedAt)

		_, redeemErr = redeem(t, store, testScope(), "+15550000001", issuance.Plaintext)
		test.ErrorIs(t, redeemErr, ErrCodeInvalid)
	})

	t.Run("hands one code to exactly one of several concurrent redemptions", func(t *testing.T) {
		const contenders = 8

		issuance := send(t, "+15550000002")

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

				if _, redeemErr := redeem(t, store, testScope(), "+15550000002", issuance.Plaintext); redeemErr == nil {
					winners.Add(1)
				}
			}()
		}

		start.Done()
		done.Wait()

		test.EqOp(t, int64(1), winners.Load())
	})

	// The attempt limit under contention: however many wrong codes race, no
	// more are counted than the limit, because each one is a compare-and-set of
	// the count it read.
	t.Run("never counts past the limit under concurrent wrong codes", func(t *testing.T) {
		const contenders = 16

		issuance := send(t, "+15550000003")

		var (
			start sync.WaitGroup
			done  sync.WaitGroup
		)

		start.Add(1)
		done.Add(contenders)

		for range contenders {
			go func() {
				defer done.Done()

				start.Wait()

				_, redeemErr := redeem(t, store, testScope(), "+15550000003", wrong(issuance.Plaintext))
				test.ErrorIs(t, redeemErr, ErrCodeInvalid)
			}()
		}

		start.Done()
		done.Wait()

		attempts := readRow(t, store, testScope(), "+15550000003").Attempts
		test.True(t, attempts >= 1 && attempts <= DefaultMaxAttempts, test.Sprintf("counted %d", attempts))
	})

	t.Run("five wrong codes kill the right one", func(t *testing.T) {
		issuance := send(t, "+15550000004")

		for range DefaultMaxAttempts {
			_, redeemErr := redeem(t, store, testScope(), "+15550000004", wrong(issuance.Plaintext))
			must.ErrorIs(t, redeemErr, ErrCodeInvalid)
		}

		_, redeemErr := redeem(t, store, testScope(), "+15550000004", issuance.Plaintext)
		test.ErrorIs(t, redeemErr, ErrCodeInvalid)
	})

	// The upsert's conflict target, which only a real engine enforces the way
	// the schema says: two issues racing for one number leave one row, and the
	// code that works is the one the later writer returned.
	t.Run("concurrent issues for one number converge on one live code", func(t *testing.T) {
		const contenders = 8

		var (
			start     sync.WaitGroup
			done      sync.WaitGroup
			mu        sync.Mutex
			issuances []*Issuance
		)

		start.Add(1)
		done.Add(contenders)

		for range contenders {
			go func() {
				defer done.Done()

				start.Wait()

				issuance, issueErr := issueFor(t, store, testScope(), &IssueRequest{SubjectID: "contact_race", PhoneNumber: "+15550000005"})
				if issueErr != nil {
					t.Errorf("issuing: %v", issueErr)

					return
				}

				mu.Lock()
				defer mu.Unlock()

				issuances = append(issuances, issuance)
			}()
		}

		start.Done()
		done.Wait()

		held := readRow(t, store, testScope(), "+15550000005")

		var survivor *Issuance
		for _, issuance := range issuances {
			if issuance.Code.ID == held.ID {
				survivor = issuance
			}
		}

		must.NotNil(t, survivor)

		_, redeemErr := redeem(t, store, testScope(), "+15550000005", survivor.Plaintext)
		test.NoError(t, redeemErr)

		codes, listErr := store.ListForSubject(ctx, client.Reader(), testScope(), "contact_race")
		must.NoError(t, listErr)
		test.SliceLen(t, 1, codes)
	})

	t.Run("a second issue supersedes the first", func(t *testing.T) {
		first := send(t, "+15550000006")
		second := send(t, "+15550000006")

		must.NotNil(t, second.Previous)
		test.EqOp(t, first.Code.ID, second.Previous.ID)

		if first.Plaintext != second.Plaintext {
			_, redeemErr := redeem(t, store, testScope(), "+15550000006", first.Plaintext)
			test.ErrorIs(t, redeemErr, ErrCodeInvalid)
		}

		_, redeemErr := redeem(t, store, testScope(), "+15550000006", second.Plaintext)
		test.NoError(t, redeemErr)
	})

	t.Run("keeps one tenant's code out of another's reach", func(t *testing.T) {
		issuance, issueErr := issueFor(t, store, tenancy.Of("tenant_scoped"),
			&IssueRequest{SubjectID: "contact_scoped", PhoneNumber: "+15550000007"})
		must.NoError(t, issueErr)

		_, redeemErr := redeem(t, store, tenancy.Of("tenant_other"), "+15550000007", issuance.Plaintext)
		test.ErrorIs(t, redeemErr, ErrCodeInvalid)

		_, redeemErr = redeem(t, store, tenancy.Of("tenant_scoped"), "+15550000007", issuance.Plaintext)
		test.NoError(t, redeemErr)
	})

	// The deadline guard is a real temporal comparison on the servers and a
	// string comparison on SQLite.
	t.Run("refuses a code past its deadline", func(t *testing.T) {
		issuance := send(t, "+15550000008")

		c.advance(DefaultLifetime + time.Second)
		defer c.advance(-(DefaultLifetime + time.Second))

		_, redeemErr := redeem(t, store, testScope(), "+15550000008", issuance.Plaintext)
		test.ErrorIs(t, redeemErr, ErrCodeInvalid)
	})

	t.Run("sweeps only what is past its purge deadline", func(t *testing.T) {
		collectable := send(t, "+15550000009")

		c.advance(DefaultLifetime + DefaultRetention + time.Second)

		surviving := send(t, "+15550000010")

		swept, sweepErr := store.Sweep(ctx)
		must.NoError(t, sweepErr)
		test.True(t, swept >= 1, test.Sprintf("swept %d", swept))

		_, redeemErr := redeem(t, store, testScope(), "+15550000009", collectable.Plaintext)
		test.ErrorIs(t, redeemErr, ErrCodeInvalid)

		_, redeemErr = redeem(t, store, testScope(), "+15550000010", surviving.Plaintext)
		test.NoError(t, redeemErr)
	})

	t.Run("withdraws one person's codes and leaves their neighbour's", func(t *testing.T) {
		mine, issueErr := issueFor(t, store, testScope(), &IssueRequest{SubjectID: "contact_revoke", PhoneNumber: "+15550000011"})
		must.NoError(t, issueErr)
		alsoMine, issueErr := issueFor(t, store, testScope(), &IssueRequest{SubjectID: "contact_revoke", PhoneNumber: "+15550000012"})
		must.NoError(t, issueErr)
		theirs := send(t, "+15550000013")

		revoked, revokeErr := revokeForSubject(t, store, testScope(), "contact_revoke")
		must.NoError(t, revokeErr)
		test.EqOp(t, int64(2), revoked)

		_, redeemErr := redeem(t, store, testScope(), "+15550000011", mine.Plaintext)
		test.ErrorIs(t, redeemErr, ErrCodeInvalid)
		_, redeemErr = redeem(t, store, testScope(), "+15550000012", alsoMine.Plaintext)
		test.ErrorIs(t, redeemErr, ErrCodeInvalid)
		_, redeemErr = redeem(t, store, testScope(), "+15550000013", theirs.Plaintext)
		test.NoError(t, redeemErr)
	})

	t.Run("erases one person's codes", func(t *testing.T) {
		_, issueErr := issueFor(t, store, testScope(), &IssueRequest{SubjectID: "contact_erase", PhoneNumber: "+15550000014"})
		must.NoError(t, issueErr)

		var deleted int64
		must.NoError(t, client.WithTransaction(ctx, func(tx database.Tx) error {
			var deleteErr error
			deleted, deleteErr = store.DeleteForSubject(ctx, tx, testScope(), "contact_erase")

			return deleteErr
		}))
		test.EqOp(t, int64(1), deleted)

		codes, listErr := store.ListForSubject(ctx, client.Reader(), testScope(), "contact_erase")
		must.NoError(t, listErr)
		test.SliceEmpty(t, codes)
	})

	t.Run("serves a namespaced table alongside the plain one", func(t *testing.T) {
		createTable(t, client, d, "app")

		namespaced, storeErr := NewSQLStore(client, WithClock(c), WithTablePrefix("app"))
		must.NoError(t, storeErr)

		issuance, issueErr := issueFor(t, namespaced, testScope(), &IssueRequest{SubjectID: "contact_ns", PhoneNumber: "+15550000015"})
		must.NoError(t, issueErr)

		_, redeemErr := redeem(t, store, testScope(), "+15550000015", issuance.Plaintext)
		test.ErrorIs(t, redeemErr, ErrCodeInvalid)

		_, redeemErr = redeem(t, namespaced, testScope(), "+15550000015", issuance.Plaintext)
		test.NoError(t, redeemErr)
	})
}

// withSQLiteClient hands fn a client over a fresh SQLite file, shaped like the
// container helpers so the three dialects' suites read alike.
func withSQLiteClient(tb testing.TB, fn func(database.Client)) {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "suite.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	fn(client)
}

func TestPhoneCodes_SQLite(T *testing.T) {
	T.Parallel()

	withSQLiteClient(T, func(client database.Client) {
		runDialectSuite(T, client, dialect.SQLite)
	})
}

func TestPhoneCodes_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.Postgres)
	})
}

func TestPhoneCodes_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("phonecodetest", "phonecodetest", "phonecodetest"))
}

// TestPhoneCodes_MySQLClientFoundRows is the MySQL suite over a connection that
// counts rows matched rather than rows changed. The generated querier offers
// clientFoundRows=true as a consumer's remedy for MySQL's changed-row count, so
// every guarded write here has to answer the same under either count — which
// they do because each one always changes the column it assigns.
func TestPhoneCodes_MySQLClientFoundRows(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		separator := "?"
		if strings.Contains(my.ConnectionString, "?") {
			separator = "&"
		}

		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{
			connectionString: my.ConnectionString + separator + "clientFoundRows=true",
			maxOpenConns:     8,
		})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("phonecodetest", "phonecodetest", "phonecodetest"))
}
