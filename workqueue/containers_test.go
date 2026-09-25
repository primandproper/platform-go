package workqueue

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/workqueue/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The unit tests above render SQL and merge batches; nothing there can tell
// whether the statements are accepted, whether SKIP LOCKED actually keeps two
// claimers apart, or whether a lease really lapses on the server's clock. That
// is the whole of what this file covers, and it is the only place that can.

// testClientConfig is the minimum database.ClientConfig a client needs. The pool
// is deliberately larger than one connection: the properties worth testing here
// are all concurrent. (SQLite's writer is one connection whatever this says,
// which is SQLite's own answer to the same question.)
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 8 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 16 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// defaultMySQLImage pins the MariaDB flavor the MySQL suite runs against, which
// is the one this module's other MySQL suites run against; mysqltest's default
// is stock MySQL.
const defaultMySQLImage = "mariadb:11"

// everyDialect is the roster the suites below run against.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// withClient hands fn a client over a live database of the dialect: a
// container for Postgres and MySQL, which skip unless RUN_CONTAINER_TESTS is
// set, and a file in a directory the test owns for SQLite, which needs no
// container and always runs.
func withClient(t *testing.T, d dialect.Dialect, fn func(client database.Client)) {
	t.Helper()

	switch d {
	case dialect.Postgres:
		pgtest.Run(t, func(ctx context.Context, pg *pgtest.Instance) {
			client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			fn(client)
		})
	case dialect.MySQL:
		mysqltest.Run(t, func(ctx context.Context, my *mysqltest.Instance) {
			client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			fn(client)
		},
			mysqltest.WithImage(defaultMySQLImage),
			mysqltest.WithCredentials("workqueuetest", "workqueuetest", "workqueuetest"),
		)
	case dialect.SQLite:
		client, err := sqlite.NewDatabaseClient(t.Context(),
			&testClientConfig{connectionString: filepath.Join(t.TempDir(), "workqueue.db")})
		must.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })

		fn(client)
	default:
		t.Fatalf("no test database for dialect %q", d)
	}
}

// queueCounter names a fresh logical queue per subtest. Subtests share one
// table, so they must not share a queue — one test's backlog would be another's.
// That they can share the table at all is itself the property Config.Name
// exists for.
var queueCounter atomic.Uint64

// createTable renders and executes the shipped DDL under a namespace.
func createTable(t *testing.T, client database.Client, prefix string) {
	t.Helper()

	stmts, err := migrations.Statements(client.Dialect(), prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}
}

// newQueue builds a Queue on its own logical queue, closed when the test ends.
func newQueue(t *testing.T, client database.Client, mutate func(*Config)) *Queue[string] {
	t.Helper()

	cfg := &Config{Name: fmt.Sprintf("q%d", queueCounter.Add(1))}
	if mutate != nil {
		mutate(cfg)
	}

	q, err := New[string](t.Context(), cfg, client)
	must.NoError(t, err)

	t.Cleanup(func() { _ = q.Close(context.WithoutCancel(t.Context())) })

	return q
}

// claimedKeys collects the keys out of a claim, for the assertions that care
// about which items came back rather than about their metadata.
func claimedKeys(items []Item[string]) []string {
	keys := make([]string, 0, len(items))
	for i := range items {
		keys = append(keys, items[i].Key)
	}

	return keys
}

// TestWorkQueue_Containers runs every suite in this file against every dialect,
// one server apiece. The suites are the same suites on all three: nothing a
// caller can observe is allowed to differ, and a dialect whose statements are
// shaped differently is exactly the one that needs the same assertions.
func TestWorkQueue_Containers(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(T *testing.T) {
			T.Parallel()

			withClient(T, d, func(client database.Client) {
				createTable(T, client, DefaultTablePrefix)

				for name, suite := range map[string]func(*testing.T, database.Client){
					"queue":                         runQueueSuite,
					"extend":                        runExtendSuite,
					"runner under a slow handler":   runRunnerUnderASlowHandler,
					"a claim fills its batch":       runClaimFillsItsBatch,
					"struct keys":                   runStructKeys,
					"migrations run twice verbatim": runMigrationsTwice,
					"a lease is never shorter":      runLeaseIsNeverShorterThanAsked,
				} {
					T.Run(name, func(t *testing.T) {
						t.Parallel()

						suite(t, client)
					})
				}
			})
		})
	}
}

//nolint:maintidx // one behavioral contract per subtest; splitting it would only hide the list.
func runQueueSuite(t *testing.T, client database.Client) {
	t.Helper()

	t.Run("enqueue, claim, complete", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)

		must.NoError(t, q.EnqueueKeys(t.Context(), "a", "b"))

		items, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 2, items)
		test.Eq(t, []string{"a", "b"}, claimedKeys(items))

		// A first claim is attempt one, and nothing has lapsed to get here.
		for i := range items {
			test.EqOp(t, 1, items[i].Attempts)
			test.False(t, items[i].Reclaimed)
		}

		must.NoError(t, q.Complete(t.Context(), items...))

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), stats.Pending)
		test.EqOp(t, int64(2), stats.Completed)
	})

	// The lease is the whole point: an item handed to one worker is invisible to
	// every other until it lapses or is given back.
	t.Run("a leased item is not claimed again", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "only"))

		first, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, first)

		second, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, second)
	})

	// Failure recovery, in full: nothing detects the dead worker, the lease
	// simply runs out on the server's clock and somebody else picks the item up.
	t.Run("a lapsed lease returns the item, flagged", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "abandoned"))

		first, err := q.Claim(t.Context(), 10, 200*time.Millisecond)
		must.NoError(t, err)
		must.SliceLen(t, 1, first)

		time.Sleep(400 * time.Millisecond)

		second, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, second)
		test.EqOp(t, "abandoned", second[0].Key)
		test.EqOp(t, 2, second[0].Attempts)
		test.True(t, second[0].Reclaimed)
	})

	// The other half of a lapsed lease: what the worker that lost it is allowed
	// to write when it finally finishes.
	//
	// Two workers, one item, and the first one slow rather than dead. Every
	// instant here is the server's — the lease runs out on its clock and the
	// sleep is only this process waiting for that to have happened — which is
	// what makes the interleaving arrangeable rather than raced for.
	t.Run("a straggler cannot report an outcome for an item somebody else took", func(t *testing.T) {
		t.Parallel()

		// lapse claims an item, lets the lease run out, reclaims it, and hands
		// back both claims' view of the one row. The first is the straggler.
		lapse := func(t *testing.T, key string) (q *Queue[string], straggler, holder Item[string]) {
			t.Helper()

			q = newQueue(t, client, nil)
			must.NoError(t, q.EnqueueKeys(t.Context(), key))

			first, err := q.Claim(t.Context(), 10, 200*time.Millisecond)
			must.NoError(t, err)
			must.SliceLen(t, 1, first)

			time.Sleep(400 * time.Millisecond)

			second, err := q.Claim(t.Context(), 10, time.Hour)
			must.NoError(t, err)
			must.SliceLen(t, 1, second)
			must.True(t, second[0].Reclaimed)

			// One row, two claims, two names. Nothing else about the row
			// distinguishes them — the key is the same and so is everything
			// derived from it, which is why the name had to be added.
			must.EqOp(t, first[0].Key, second[0].Key)
			must.NotEqOp(t, "", first[0].LeasedBy)
			must.NotEqOp(t, first[0].LeasedBy, second[0].LeasedBy)

			return q, first[0], second[0]
		}

		t.Run("its completion retires nothing", func(t *testing.T) {
			t.Parallel()

			q, straggler, holder := lapse(t, "slow-completer")

			must.NoError(t, q.Complete(t.Context(), straggler))

			// Still outstanding. Retiring it here would record the work as done
			// before the second worker did it — and then exclude that worker's
			// own release as already-completed, which is how the item would be
			// lost rather than merely done twice.
			stats, err := q.Stats(t.Context())
			must.NoError(t, err)
			test.EqOp(t, int64(0), stats.Completed)
			test.EqOp(t, int64(1), stats.Pending)

			// The holder's own completion lands, so what the fence refused was
			// the straggler rather than the statement.
			must.NoError(t, q.Complete(t.Context(), holder))

			stats, err = q.Stats(t.Context())
			must.NoError(t, err)
			test.EqOp(t, int64(1), stats.Completed)
		})

		t.Run("its release hands nothing back", func(t *testing.T) {
			t.Parallel()

			q, straggler, _ := lapse(t, "slow-releaser")

			must.NoError(t, q.Release(t.Context(), 0, platformerrors.New("straggler"), straggler))

			// The holder's lease is intact: a hand-back here would put the item
			// in front of a third worker while the second is still doing it.
			stats, err := q.Stats(t.Context())
			must.NoError(t, err)
			test.EqOp(t, int64(1), stats.Leased)
			test.EqOp(t, int64(0), stats.Ready)
		})
	})

	// The complement, and the reason the fence is the claim's name rather than
	// the lease's liveness: a worker whose lease ran out with nobody else
	// claiming still holds the item, and the work it did is recorded rather
	// than done again.
	t.Run("a lapsed lease nobody else took still completes", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "slow-but-alone"))

		claimed, err := q.Claim(t.Context(), 10, 200*time.Millisecond)
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		time.Sleep(400 * time.Millisecond)

		must.NoError(t, q.Complete(t.Context(), claimed...))

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), stats.Completed)
	})

	// Completing releases the lease too, so a restarted item is immediately
	// claimable rather than waiting out the lease it was completed under.
	t.Run("completing an item releases its lease", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "recycled"))

		claimed, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.NoError(t, q.Complete(t.Context(), claimed...))
		must.NoError(t, q.EnqueueKeys(t.Context(), "recycled"))

		items, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, items)
		// Restarted, so the attempt count starts over rather than carrying the
		// history of a run that already succeeded.
		test.EqOp(t, 1, items[0].Attempts)
	})

	t.Run("priority beats waiting time", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)

		must.NoError(t, q.EnqueueKeys(t.Context(), "early"))
		must.NoError(t, q.Enqueue(t.Context(), Entry[string]{Key: "urgent", Priority: 10}))

		items, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 2, items)
		test.EqOp(t, "urgent", items[0].Key)
		test.EqOp(t, "early", items[1].Key)
	})

	// Re-enqueueing is how a read path expresses demand, so it has to be able to
	// promote work that is already waiting — and must never demote it.
	t.Run("re-enqueueing raises priority but cannot lower it", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)

		must.NoError(t, q.EnqueueKeys(t.Context(), "quiet"))
		must.NoError(t, q.Enqueue(t.Context(), Entry[string]{Key: "loud"}))
		must.NoError(t, q.Enqueue(t.Context(), Entry[string]{Key: "loud", Priority: 5}))
		must.NoError(t, q.Enqueue(t.Context(), Entry[string]{Key: "loud", Priority: 1}))

		items, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 2, items)
		test.EqOp(t, "loud", items[0].Key)
		test.EqOp(t, 5, items[0].Priority)
	})

	t.Run("a delayed item is held back", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)

		must.NoError(t, q.Enqueue(t.Context(),
			Entry[string]{Key: "later", Delay: time.Hour},
			Entry[string]{Key: "now"},
		))

		items, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, items)
		test.EqOp(t, "now", items[0].Key)
	})

	// The delay is one-way for an outstanding item, matching the priority rule:
	// a caller that wants it sooner wins, a caller that wants it later does not
	// get to push somebody else's work back.
	t.Run("re-enqueueing can only bring an item forward", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)

		must.NoError(t, q.Enqueue(t.Context(), Entry[string]{Key: "k", Delay: time.Hour}))

		items, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceEmpty(t, items)

		must.NoError(t, q.Enqueue(t.Context(), Entry[string]{Key: "k"}))

		items, err = q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, items)

		must.NoError(t, q.Release(t.Context(), 0, nil, items...))
		must.NoError(t, q.Enqueue(t.Context(), Entry[string]{Key: "k", Delay: time.Hour}))

		items, err = q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceLen(t, 1, items)
	})

	t.Run("release hands an item straight back", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "handed-back"))

		claimed, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)

		must.NoError(t, q.Release(t.Context(), 0, platformerrors.New("not my problem"), claimed...))

		items, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, items)
		// The attempt count survives the release, so a repeatedly failing item
		// still walks toward its ceiling.
		test.EqOp(t, 2, items[0].Attempts)
	})

	t.Run("release with a delay backs the item off", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "backed-off"))

		claimed, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)

		must.NoError(t, q.Release(t.Context(), time.Hour, platformerrors.New("try later"), claimed...))

		items, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, items)
	})

	// A late release arriving after somebody else finished the work would
	// otherwise resurrect it, and the pair would loop forever.
	t.Run("release cannot resurrect a completed item", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "done"))

		claimed, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.NoError(t, q.Complete(t.Context(), claimed...))

		// The same claim's own late release, which the completion above already
		// refuses on its own terms — the row is completed. The claim fence
		// refuses it a second way, and the two are different straggler stories;
		// see the lapsed-lease subtests below for the other one.
		must.NoError(t, q.Release(t.Context(), 0, platformerrors.New("straggler"), claimed...))

		items, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, items)
	})

	// Complete, Release and Extend take whatever Items the caller hands back,
	// and nothing stops those coming from two claims. On MySQL and SQLite that
	// is a statement per claim, each fenced on its own name; on Postgres it is
	// the pairs. Either way both claims' items land.
	t.Run("one call can report on several claims", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "x", "y", "z"))

		first, err := q.Claim(t.Context(), 1, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, first)

		second, err := q.Claim(t.Context(), 2, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 2, second)
		must.NotEqOp(t, first[0].LeasedBy, second[0].LeasedBy)

		held, err := q.Extend(t.Context(), time.Hour, append(first, second...)...)
		must.NoError(t, err)
		test.EqOp(t, int64(3), held)

		must.NoError(t, q.Complete(t.Context(), append(second, first...)...))

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), stats.Pending)
		test.EqOp(t, int64(3), stats.Completed)
	})

	t.Run("completing an unknown key is not an error", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)

		// An Item nothing handed out, which is what a key the queue never held
		// looks like from here: no row carries the key, and none carries the
		// empty name either.
		unknown := Item[string]{Key: "never-enqueued"}

		test.NoError(t, q.Complete(t.Context(), unknown))
		test.NoError(t, q.Release(t.Context(), 0, nil, unknown))
	})

	t.Run("remove drops an item whether or not it is leased", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "leased", "idle"))

		claimed, err := q.Claim(t.Context(), 1, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		must.NoError(t, q.Remove(t.Context(), "leased", "idle"))

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), stats.Pending)

		// The worker still holding the removed item can report success without
		// anything blowing up, which is what makes Remove usable at all.
		test.NoError(t, q.Complete(t.Context(), claimed...))
	})

	t.Run("reap removes completed items past retention", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, func(cfg *Config) { cfg.Retention = time.Second })

		must.NoError(t, q.EnqueueKeys(t.Context(), "old"))
		claimed, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.NoError(t, q.Complete(t.Context(), claimed...))

		// Inside the window, nothing is eligible.
		reaped, err := q.Reap(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), reaped)

		time.Sleep(1200 * time.Millisecond)

		reaped, err = q.Reap(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), reaped)

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), stats.Completed)
	})

	// Without a ceiling a poison item is claimed, half-processed and reclaimed
	// forever, and because it sorts to the front it takes the queue's throughput
	// with it.
	t.Run("an item out of attempts stalls instead of spinning", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, func(cfg *Config) { cfg.MaxAttempts = 2 })
		must.NoError(t, q.EnqueueKeys(t.Context(), "poison"))

		for attempt := 1; attempt <= 2; attempt++ {
			items, claimErr := q.Claim(t.Context(), 10, time.Hour)
			must.NoError(t, claimErr)
			must.SliceLen(t, 1, items, must.Sprintf("attempt %d", attempt))
			must.NoError(t, q.Release(t.Context(), 0, platformerrors.New("still broken"), items...))
		}

		items, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		test.SliceEmpty(t, items)

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), stats.Pending)
		test.EqOp(t, int64(1), stats.Stalled)
		test.EqOp(t, int64(0), stats.Ready)

		// Re-enqueueing does not clear the ceiling on its own — the item is
		// outstanding, so its attempts are preserved. That is what keeps the
		// ceiling a ceiling: one any read path's enqueue could lift would not be
		// one. Requeue is the way back, and it is deliberately not automatic.
		must.NoError(t, q.EnqueueKeys(t.Context(), "poison"))

		items, err = q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		test.SliceEmpty(t, items)
	})

	// The way out of the ceiling, and the only one. Without it a stalled item
	// can only be revived by Remove-then-Enqueue, which loses the enqueue time,
	// the priority and the last error the row was kept around for.
	t.Run("requeue revives a stalled item and keeps what it was kept for", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, func(cfg *Config) { cfg.MaxAttempts = 2 })
		must.NoError(t, q.Enqueue(t.Context(), Entry[string]{Key: "poison", Priority: 7}))

		for attempt := 1; attempt <= 2; attempt++ {
			items, claimErr := q.Claim(t.Context(), 10, time.Hour)
			must.NoError(t, claimErr)
			must.SliceLen(t, 1, items, must.Sprintf("attempt %d", attempt))

			// The last hand-back backs the item off by an hour, so the revival
			// has to move availability as well as the counter — a stalled item
			// is normally stalled behind the delay its final release wrote.
			backoff := time.Duration(0)
			if attempt == 2 {
				backoff = time.Hour
			}

			must.NoError(t, q.Release(t.Context(), backoff, platformerrors.New("still broken"), items...))
		}

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		must.EqOp(t, int64(1), stats.Stalled)

		revived, err := q.Requeue(t.Context(), "poison")
		must.NoError(t, err)
		test.EqOp(t, int64(1), revived)

		stats, err = q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), stats.Stalled)
		test.EqOp(t, int64(1), stats.Ready)

		items, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, items)
		test.EqOp(t, "poison", items[0].Key)

		// The counter really went back to zero rather than merely below the
		// ceiling, and the priority survived — which a Remove and a fresh
		// Enqueue would not have done.
		test.EqOp(t, 1, items[0].Attempts)
		test.EqOp(t, 7, items[0].Priority)
	})

	t.Run("requeue counts only the items it revived", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "outstanding", "finished"))

		claimed, err := q.Claim(t.Context(), 1, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)
		must.NoError(t, q.Complete(t.Context(), claimed...))

		// Three keys named, one revived: a completed item is restarted by an
		// Enqueue that carries a schedule rather than revived, and a key the
		// queue never held is nothing at all.
		revived, err := q.Requeue(t.Context(), "outstanding", claimed[0].Key, "never-enqueued")
		must.NoError(t, err)
		test.EqOp(t, int64(1), revived)

		// The completion stands: reviving must not resurrect finished work.
		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), stats.Completed)
	})

	// An item can be leased and stalled at once, when the claim that pushed the
	// count to the ceiling is still running. Reviving it must not revoke that
	// lease — the worker's outcome is still its own to report.
	t.Run("requeue leaves a live lease alone", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, func(cfg *Config) { cfg.MaxAttempts = 1 })
		must.NoError(t, q.EnqueueKeys(t.Context(), "held"))

		claimed, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		revived, err := q.Requeue(t.Context(), "held")
		must.NoError(t, err)
		test.EqOp(t, int64(1), revived)

		// Nobody else can take it, because the lease still stands.
		items, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		test.SliceEmpty(t, items)

		// And the holder's completion still lands, which is the fence saying
		// the claim it was stamped with survived.
		must.NoError(t, q.Complete(t.Context(), claimed...))

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), stats.Completed)
	})

	// The property the whole package exists for: any number of workers draining
	// one table, and no key handed to two of them.
	t.Run("concurrent claimers never share an item", func(t *testing.T) {
		t.Parallel()

		const (
			workers = 8
			items   = 200
		)

		q := newQueue(t, client, nil)

		keys := make([]string, 0, items)
		for i := range items {
			keys = append(keys, fmt.Sprintf("item-%03d", i))
		}

		must.NoError(t, q.EnqueueKeys(t.Context(), keys...))

		var (
			mu   sync.Mutex
			seen = map[string]int{}
			wg   sync.WaitGroup
		)

		for range workers {
			wg.Go(func() {
				for {
					claimed, claimErr := q.Claim(context.Background(), 7, time.Minute)
					if claimErr != nil {
						t.Error(claimErr)

						return
					}

					if len(claimed) == 0 {
						// Confirmed against the count rather than against one
						// claim: another worker may hold items that are still
						// in flight, and this one must not exit before they
						// land.
						mu.Lock()
						done := len(seen)
						mu.Unlock()

						if done >= items {
							return
						}

						continue
					}

					mu.Lock()
					for i := range claimed {
						seen[claimed[i].Key]++
					}
					mu.Unlock()

					must.NoError(t, q.Complete(context.Background(), claimed...))
				}
			})
		}

		wg.Wait()

		test.MapLen(t, items, seen)
		for key, count := range seen {
			test.EqOp(t, 1, count, test.Sprintf("key %q was claimed %d times", key, count))
		}
	})

	// Group commit under exactly the shape that wedged the original: many
	// callers upserting overlapping keys at once. Without the merge and the
	// sort, this is where 40P01 shows up.
	t.Run("concurrent enqueues of overlapping keys all land", func(t *testing.T) {
		t.Parallel()

		const (
			callers  = 24
			perCall  = 12
			universe = 20
		)

		q := newQueue(t, client, nil)

		var wg sync.WaitGroup

		for caller := range callers {
			wg.Go(func() {
				// Deliberately built in a rotated order, so two callers rarely
				// name their overlapping keys in the same sequence. That is the
				// input the lock ordering has to survive.
				keys := make([]string, 0, perCall)
				for i := range perCall {
					keys = append(keys, fmt.Sprintf("shared-%02d", (caller+i*3)%universe))
				}

				if err := q.EnqueueKeys(context.Background(), keys...); err != nil {
					t.Error(err)
				}
			})
		}

		wg.Wait()

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		// Every distinct key landed exactly once, however many callers named it.
		test.EqOp(t, int64(universe), stats.Pending)
	})

	// Read-your-write is what makes Enqueue safe to call from a request handler:
	// the caller blocks until its own keys are in, not merely until the batch it
	// joined was accepted.
	t.Run("enqueue is visible to the claim that follows it", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)

		for i := range 20 {
			key := fmt.Sprintf("rw-%02d", i)

			must.NoError(t, q.Enqueue(t.Context(), Entry[string]{Key: key, Priority: 100}))

			items, err := q.Claim(t.Context(), 1, time.Minute)
			must.NoError(t, err)
			must.SliceLen(t, 1, items)
			test.EqOp(t, key, items[0].Key)

			must.NoError(t, q.Complete(t.Context(), items...))
		}
	})

	t.Run("stats describes the queue's shape", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)

		must.NoError(t, q.Enqueue(t.Context(),
			Entry[string]{Key: "ready-1"},
			Entry[string]{Key: "ready-2"},
			Entry[string]{Key: "held", Delay: time.Hour},
		))

		_, err := q.Claim(t.Context(), 1, time.Hour)
		must.NoError(t, err)

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(3), stats.Pending)
		test.EqOp(t, int64(1), stats.Ready)
		test.EqOp(t, int64(1), stats.Leased)
		test.EqOp(t, int64(0), stats.Completed)
		test.EqOp(t, int64(0), stats.Stalled)
	})

	// The age is what separates a queue that is deep and draining from one that
	// is deep and stuck, and it has to be measured on the server's clock rather
	// than against anything this process holds.
	t.Run("the oldest ready age grows with the wait", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "waiting"))

		time.Sleep(600 * time.Millisecond)

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.True(t, stats.OldestReadyAge >= 500*time.Millisecond,
			test.Sprintf("oldest ready age was %s", stats.OldestReadyAge))

		// Drained the way a worker drains it — claimed, then completed. There is
		// no longer a way to retire an item without holding it, which is the
		// point of the fence: Complete reports on a claim.
		claimed, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		must.NoError(t, q.Complete(t.Context(), claimed...))

		stats, err = q.Stats(t.Context())
		must.NoError(t, err)
		// A drained queue actively reports zero rather than leaving the last
		// reading on the dashboard.
		test.EqOp(t, time.Duration(0), stats.OldestReadyAge)
	})

	// One table, many queues. Nothing else in the package would notice if the
	// name were dropped from a predicate, and the consequence would be one
	// application draining another's work.
	t.Run("queues sharing a table do not see each other", func(t *testing.T) {
		t.Parallel()

		first := newQueue(t, client, nil)
		second := newQueue(t, client, nil)

		must.NoError(t, first.EnqueueKeys(t.Context(), "shared-key"))

		items, err := second.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, items)

		// The same key in both queues is two independent items.
		must.NoError(t, second.EnqueueKeys(t.Context(), "shared-key"))

		items, err = second.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, items)

		items, err = first.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceLen(t, 1, items)
	})

	// A batch crosses the seam as one array per column rather than as a tuple
	// per row, so the nth key, the nth priority and the nth delay have to still
	// describe one entry at the other end. Nothing in a rendered statement can
	// say whether they do — a pairing that slipped by one would produce a
	// statement that runs, writes every row, and gets each one wrong.
	t.Run("a batch pairs each key with its own priority and delay", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)

		// Deliberately out of key order, and deliberately not in priority order
		// either: the merge, the sort and the split that follow are what the
		// pairing has to survive.
		must.NoError(t, q.Enqueue(t.Context(),
			Entry[string]{Key: "c", Priority: 2},
			Entry[string]{Key: "a", Priority: 7},
			Entry[string]{Key: "d", Priority: 9, Delay: time.Hour},
			Entry[string]{Key: "b", Priority: 4},
		))

		items, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)

		// d is the loudest and would lead the batch, so its absence is the
		// delay landing on the row it was bound beside rather than on another.
		must.SliceLen(t, 3, items)
		test.Eq(t, []string{"a", "b", "c"}, claimedKeys(items))

		byKey := map[string]int{}
		for i := range items {
			byKey[items[i].Key] = items[i].Priority
		}

		test.Eq(t, map[string]int{"a": 7, "b": 4, "c": 2}, byKey)
	})

	t.Run("a claim is capped by the configured batch", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, func(cfg *Config) { cfg.MaxClaimBatch = 3 })

		must.NoError(t, q.EnqueueKeys(t.Context(), "a", "b", "c", "d", "e"))

		items, err := q.Claim(t.Context(), 100, time.Minute)
		must.NoError(t, err)
		test.SliceLen(t, 3, items)

		// A non-positive limit means "as many as allowed" rather than "none".
		items, err = q.Claim(t.Context(), 0, time.Minute)
		must.NoError(t, err)
		test.SliceLen(t, 2, items)
	})
}

// The lease extension, against the clock that actually governs it. Every
// property below is the server's answer rather than this process's: whether an
// extended lease is still held after the original would have lapsed, whether an
// extension from a claim the row no longer names moves anything, and whether a
// shorter extension can pull a longer lease in.
func runExtendSuite(t *testing.T, client database.Client) {
	t.Helper()

	t.Run("an extended lease outlives the one it was claimed under", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "slow"))

		claimed, err := q.Claim(t.Context(), 10, 300*time.Millisecond)
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		held, err := q.Extend(t.Context(), time.Hour, claimed...)
		must.NoError(t, err)
		test.EqOp(t, int64(1), held)

		// Well past the lease the claim was taken under. Without the
		// extension this is exactly the sleep that hands the item to
		// somebody else.
		time.Sleep(600 * time.Millisecond)

		competitor, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, competitor)

		// And the claim that extended still owns the item, so its completion
		// lands.
		must.NoError(t, q.Complete(t.Context(), claimed...))

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), stats.Completed)
	})

	// The fence, read a third way: a straggler pushing out a horizon it no
	// longer holds would pin the item to a claim nobody is working under.
	t.Run("a straggler extends nothing", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "taken"))

		straggler, err := q.Claim(t.Context(), 10, 200*time.Millisecond)
		must.NoError(t, err)
		must.SliceLen(t, 1, straggler)

		time.Sleep(400 * time.Millisecond)

		holder, err := q.Claim(t.Context(), 10, 500*time.Millisecond)
		must.NoError(t, err)
		must.SliceLen(t, 1, holder)
		must.True(t, holder[0].Reclaimed)

		held, err := q.Extend(t.Context(), time.Hour, straggler...)
		must.NoError(t, err)
		test.EqOp(t, int64(0), held)

		// The holder's own lease is untouched by that, so it lapses on its
		// own schedule and the item comes back — rather than being parked
		// for the hour the straggler asked for.
		time.Sleep(700 * time.Millisecond)

		back, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceLen(t, 1, back)
	})

	// GREATEST, on the server: an extension shorter than what is left on the
	// lease changes nothing rather than pulling the horizon in.
	t.Run("an extension never shortens a lease", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "long-lease"))

		claimed, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		held, err := q.Extend(t.Context(), 200*time.Millisecond, claimed...)
		must.NoError(t, err)
		test.EqOp(t, int64(1), held)

		time.Sleep(400 * time.Millisecond)

		competitor, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, competitor)
	})

	// A completed item is excluded inside the CTE, so an extension that
	// arrives after the work was retired matches nothing rather than
	// resurrecting a horizon on a finished row.
	t.Run("a completed item is not extended", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "done"))

		claimed, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)
		must.NoError(t, q.Complete(t.Context(), claimed...))

		held, err := q.Extend(t.Context(), time.Hour, claimed...)
		must.NoError(t, err)
		test.EqOp(t, int64(0), held)
	})
}

// The runner, end to end, against the property the whole extension exists for:
// a handler that runs for longer than the lease it was claimed under still holds
// its item when it finishes, and nobody else has run it in the meantime.
//
// It needs a real server because the lease is the server's clock: nothing a
// stubbed querier can say distinguishes an item that is still leased from one
// that has been handed to somebody else.
func runRunnerUnderASlowHandler(t *testing.T, client database.Client) {
	t.Helper()

	ctx := t.Context()

	q := newQueue(t, client, nil)
	must.NoError(t, q.EnqueueKeys(ctx, "slow-work"))

	var handled atomic.Int64

	// A lease of a second under a handler that takes two and a half, with the
	// heartbeat at a third of the lease. Every one of those numbers is a real
	// one: without the extension this handler is reclaimed twice over while
	// it works.
	cfg := &RunnerConfig{
		Poll:           50 * time.Millisecond,
		Lease:          time.Second,
		ExtendInterval: 300 * time.Millisecond,
		Batch:          10,
		Concurrency:    1,
	}

	runner, err := NewRunner(ctx, cfg, q, func(context.Context, Item[string]) error {
		handled.Add(1)

		time.Sleep(2500 * time.Millisecond)

		return nil
	})
	must.NoError(t, err)

	runCtx, stop := context.WithCancel(ctx)

	done := make(chan error, 1)
	go func() { done <- runner.Run(runCtx) }()

	// A competitor claiming throughout, which is what a second worker in the
	// fleet is. None of its claims may find the item, because the runner is
	// still working on it.
	competitor := newQueue(t, client, func(c *Config) { c.Name = q.Name() })

	for range 10 {
		time.Sleep(200 * time.Millisecond)

		stolen, claimErr := competitor.Claim(ctx, 10, time.Minute)
		must.NoError(t, claimErr)
		test.SliceEmpty(t, stolen, test.Sprintf("the item was reclaimed mid-handler: %v", claimedKeys(stolen)))
	}

	// The handler outlived its original lease and still retired its own item.
	waitForCompletion(t, q)

	stop()
	must.ErrorIs(t, awaitRunner(t, done), context.Canceled)

	test.EqOp(t, int64(1), handled.Load())
}

// waitForCompletion blocks until the queue reports the item retired, so the
// assertion is on the row rather than on a sleep long enough to hope for it.
func waitForCompletion(t *testing.T, q *Queue[string]) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		stats, err := q.Stats(t.Context())
		must.NoError(t, err)

		if stats.Completed == 1 && stats.Pending == 0 {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatal("the runner never completed the item it claimed")
}

// awaitRunner collects Run's error, failing rather than hanging if the loop does
// not notice its context.
func awaitRunner(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the runner did not stop on a cancelled context")

		return nil
	}
}

// The claim's LIMIT sits above the lock, so a row another transaction holds is
// skipped and replaced rather than counted against the batch. That is what lets
// a fleet of claimers each get full batches instead of dividing one, and it is a
// property of the statement's shape rather than of SKIP LOCKED itself — pushing
// the LIMIT into a subquery beneath the lock would still be correct and would
// quietly halve throughput under contention.
//
// It needs a real server and a second connection holding locks open, which is
// why it lives here and not beside the other claim tests. On MySQL it is the
// claim's first statement that has to skip and replace, which is the reason to
// run it there rather than assume it: MySQL counts the LIMIT after SKIP LOCKED
// has skipped, but only if the read walks the index in the claim's order —
// sorting would lock every candidate before the limit was applied.
//
// SQLite has one writer, so there is no claimer frozen mid-claim to skip around:
// a transaction holding rows there holds the whole database. What stands in for
// the competitor is the only thing SQLite can have, a claim that already
// committed, and the property left to pin is the same count read the other way
// — held rows are replaced rather than subtracted.
func runClaimFillsItsBatch(t *testing.T, client database.Client) {
	t.Helper()

	ctx := t.Context()

	createTable(t, client, "contention")

	q, err := New[string](ctx, &Config{Name: "contended", TablePrefix: "contention"}, client)
	must.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(context.WithoutCancel(ctx)) })

	keys := make([]string, 0, 10)
	for i := range 10 {
		keys = append(keys, fmt.Sprintf("k%02d", i))
	}

	must.NoError(t, q.EnqueueKeys(ctx, keys...))

	held := holdThree(t, client, q)

	// Five asked for, three unavailable, seven left to choose from: a full
	// five come back.
	claimed, err := q.Claim(ctx, 5, time.Minute)
	must.NoError(t, err)
	test.SliceLen(t, 5, claimed)

	// And none of them is a row the other claimer is holding, which is SKIP
	// LOCKED doing its half of the job.
	for i := range claimed {
		test.SliceNotContains(t, held, claimed[i].Key)
	}
}

// holdThree puts three of the contended queue's items out of a claim's reach
// and names them: locked by a transaction left open where the engine has row
// locks, and leased by a committed claim where it does not.
func holdThree(t *testing.T, client database.Client, q *Queue[string]) []string {
	t.Helper()

	ctx := t.Context()

	if client.Dialect() == dialect.SQLite {
		leased, err := q.Claim(ctx, 3, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 3, leased)

		return claimedKeys(leased)
	}

	// A competing claimer, frozen mid-claim: three rows locked and not yet
	// released.
	raw, ok := client.(database.RawAccess)
	must.True(t, ok, must.Sprintf("%T exposes no pool to hold a transaction open on", client))

	tx, err := raw.WriteDB().BeginTx(ctx, nil)
	must.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	rows, err := tx.QueryContext(ctx,
		"SELECT item_key FROM contention_work_queue_items WHERE queue_name = "+client.Dialect().Placeholder(1)+" "+
			"ORDER BY item_key LIMIT 3 FOR UPDATE SKIP LOCKED", "contended")
	must.NoError(t, err)

	var locked []string

	for rows.Next() {
		var key string
		must.NoError(t, rows.Scan(&key))

		locked = append(locked, key)
	}

	must.NoError(t, rows.Err())
	must.NoError(t, rows.Close())
	must.Eq(t, []string{"k00", "k01", "k02"}, locked)

	return locked
}

// Struct keys are the shape the extraction came from — a composite identifier,
// not a string — so the JSON codec is exercised against a real column rather
// than only in memory.
func runStructKeys(t *testing.T, client database.Client) {
	t.Helper()

	ctx := t.Context()

	createTable(t, client, "structkeys")

	q, err := New[pairKey](ctx, &Config{Name: "pairs", TablePrefix: "structkeys"}, client)
	must.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(context.WithoutCancel(ctx)) })

	want := []pairKey{
		{Profile: "car", Origin: 1, Dest: 2},
		{Profile: "bike", Origin: 1, Dest: 2},
		{Profile: "car", Origin: 2, Dest: 1},
	}

	entries := make([]Entry[pairKey], 0, len(want))
	for i := range want {
		entries = append(entries, Entry[pairKey]{Key: want[i]})
	}

	must.NoError(t, q.Enqueue(ctx, entries...))

	items, err := q.Claim(ctx, 10, time.Minute)
	must.NoError(t, err)
	must.SliceLen(t, len(want), items)

	got := make([]pairKey, 0, len(items))
	for i := range items {
		got = append(got, items[i].Key)
	}

	for i := range want {
		test.SliceContains(t, got, want[i])
	}

	must.NoError(t, q.Complete(ctx, items...))

	stats, err := q.Stats(ctx)
	must.NoError(t, err)
	test.EqOp(t, int64(0), stats.Pending)
}

// runMigrationsTwice proves the shipped DDL is accepted verbatim, and that
// re-running it is a no-op — the property every statement's IF NOT EXISTS is
// there for, and the one a consumer's migration runner depends on.
func runMigrationsTwice(t *testing.T, client database.Client) {
	t.Helper()

	stmts, err := migrations.Statements(client.Dialect(), "ddl_check")
	must.NoError(t, err)

	for range 2 {
		for _, stmt := range stmts {
			_, execErr := client.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
		}
	}

	for _, stmt := range stmts {
		test.False(t, strings.Contains(stmt, "{{"))
	}
}

// runLeaseIsNeverShorterThanAsked pins the one direction a clock's precision
// must never round in: a lease that ends before it was asked to is a lease
// another worker takes over while this one is still working.
//
// SQLite is the engine it is about — its clock is millisecond text, so every
// duration is rounded, and its now is truncated before anything is added to it
// — and it runs on all three because the property is the package's rather than
// SQLite's. Each lease is odd on purpose: a microsecond count that is not a
// whole millisecond is the one a rounding down would shorten.
//
// The measure is the database's own clock, read before the claim. That read is
// no later than the instant the claim measured from, so a horizon at least the
// lease past it is a horizon at least the lease past the claim — and the
// reading is taken in the stored shape, so the subtraction below is between two
// values the same clock wrote.
func runLeaseIsNeverShorterThanAsked(t *testing.T, client database.Client) {
	t.Helper()

	ctx := t.Context()
	d := client.Dialect()

	for i, lease := range []time.Duration{
		time.Microsecond,
		999 * time.Microsecond,
		1001 * time.Microsecond,
		1500*time.Millisecond + time.Microsecond,
		2*time.Second + 999999*time.Microsecond,
	} {
		q := newQueue(t, client, nil)
		key := fmt.Sprintf("lease-%d", i)

		must.NoError(t, q.EnqueueKeys(ctx, key))

		before := serverNow(t, client)

		claimed, err := q.Claim(ctx, 1, lease)
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		until := storedInstant(t, client, q.Name(), key, "lease_until")
		test.True(t, until.Sub(before) >= lease,
			test.Sprintf("%s: a %s lease ended %s after a clock read taken before the claim", d, lease, until.Sub(before)))

		// An extension is a lease too, measured from its own now.
		before = serverNow(t, client)

		_, err = q.Extend(ctx, lease+time.Hour, claimed...)
		must.NoError(t, err)

		until = storedInstant(t, client, q.Name(), key, "lease_until")
		test.True(t, until.Sub(before) >= lease+time.Hour,
			test.Sprintf("%s: a %s extension ended %s after a clock read taken before it", d, lease+time.Hour, until.Sub(before)))
	}
}

// instantLayouts are the shapes the three engines hand an instant back in when
// it is read as text: SQLite's is what strftime's %f writes, and the other two
// are what a driver renders a timestamp column as.
var instantLayouts = []string{
	"2006-01-02 15:04:05.000",
	"2006-01-02 15:04:05.999999",
	time.RFC3339Nano,
}

// serverNow reads the database's clock the way the queue's statements read it.
func serverNow(t *testing.T, client database.Client) time.Time {
	t.Helper()

	query := map[dialect.Dialect]string{
		dialect.Postgres: "SELECT to_char(CURRENT_TIMESTAMP AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')",
		dialect.MySQL:    "SELECT DATE_FORMAT(CURRENT_TIMESTAMP(6), '%Y-%m-%d %H:%i:%s.%f')",
		dialect.SQLite:   "SELECT strftime('%Y-%m-%d %H:%M:%f', 'now')",
	}[client.Dialect()]

	var text string
	must.NoError(t, client.Writer().QueryRowContext(t.Context(), query).Scan(&text))

	return parseInstant(t, text)
}

// storedInstant reads one of an item's instants back, rendered as text in the
// same shape serverNow reads the clock in.
func storedInstant(t *testing.T, client database.Client, queue, key, column string) time.Time {
	t.Helper()

	d := client.Dialect()

	projection := map[dialect.Dialect]string{
		dialect.Postgres: "to_char(" + column + " AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US')",
		dialect.MySQL:    "DATE_FORMAT(" + column + ", '%Y-%m-%d %H:%i:%s.%f')",
		dialect.SQLite:   column,
	}[d]

	var text string
	must.NoError(t, client.Writer().QueryRowContext(t.Context(),
		"SELECT "+projection+" FROM work_queue_items WHERE queue_name = "+d.Placeholder(1)+
			" AND item_key = "+d.Placeholder(2), queue, key).Scan(&text))

	return parseInstant(t, text)
}

func parseInstant(t *testing.T, text string) time.Time {
	t.Helper()

	for _, layout := range instantLayouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed
		}
	}

	t.Fatalf("unparseable instant %q", text)

	return time.Time{}
}
