package workqueue

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/postgres"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/testutils/containers/pgtest"
	"github.com/primandproper/platform-go/v13/workqueue/migrations"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The unit tests above render SQL and merge batches; nothing there can tell
// whether the statements are accepted, whether SKIP LOCKED actually keeps two
// claimers apart, or whether a lease really lapses on the server's clock. That
// is the whole of what this file covers, and it is the only place that can.

// testClientConfig is the minimum database.ClientConfig a Postgres client needs.
// The pool is deliberately larger than one connection: the properties worth
// testing here are all concurrent.
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

// queueCounter names a fresh logical queue per subtest. Subtests share one
// table, so they must not share a queue — one test's backlog would be another's.
// That they can share the table at all is itself the property Config.Name
// exists for.
var queueCounter atomic.Uint64

// createTable renders and executes the shipped DDL under a namespace.
func createTable(t *testing.T, client database.Client, prefix string) {
	t.Helper()

	stmts, err := migrations.Statements(dialect.Postgres, prefix)
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

func TestWorkQueue_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		createTable(T, client, DefaultTablePrefix)

		runQueueSuite(T, client)
	})
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

		must.NoError(t, q.Complete(t.Context(), "a", "b"))

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

	// Completing releases the lease too, so a restarted item is immediately
	// claimable rather than waiting out the lease it was completed under.
	t.Run("completing an item releases its lease", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "recycled"))

		_, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.NoError(t, q.Complete(t.Context(), "recycled"))
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

		must.NoError(t, q.Release(t.Context(), 0, nil, "k"))
		must.NoError(t, q.Enqueue(t.Context(), Entry[string]{Key: "k", Delay: time.Hour}))

		items, err = q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceLen(t, 1, items)
	})

	t.Run("release hands an item straight back", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "handed-back"))

		_, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)

		must.NoError(t, q.Release(t.Context(), 0, platformerrors.New("not my problem"), "handed-back"))

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

		_, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)

		must.NoError(t, q.Release(t.Context(), time.Hour, platformerrors.New("try later"), "backed-off"))

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

		_, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.NoError(t, q.Complete(t.Context(), "done"))
		must.NoError(t, q.Release(t.Context(), 0, platformerrors.New("straggler"), "done"))

		items, err := q.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, items)
	})

	t.Run("completing an unknown key is not an error", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)

		test.NoError(t, q.Complete(t.Context(), "never-enqueued"))
		test.NoError(t, q.Release(t.Context(), 0, nil, "never-enqueued"))
	})

	t.Run("remove drops an item whether or not it is leased", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, nil)
		must.NoError(t, q.EnqueueKeys(t.Context(), "leased", "idle"))

		_, err := q.Claim(t.Context(), 1, time.Hour)
		must.NoError(t, err)

		must.NoError(t, q.Remove(t.Context(), "leased", "idle"))

		stats, err := q.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), stats.Pending)

		// The worker still holding the removed item can report success without
		// anything blowing up, which is what makes Remove usable at all.
		test.NoError(t, q.Complete(t.Context(), "leased"))
	})

	t.Run("reap removes completed items past retention", func(t *testing.T) {
		t.Parallel()

		q := newQueue(t, client, func(cfg *Config) { cfg.Retention = time.Second })

		must.NoError(t, q.EnqueueKeys(t.Context(), "old"))
		_, err := q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.NoError(t, q.Complete(t.Context(), "old"))

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
			must.NoError(t, q.Release(t.Context(), 0, platformerrors.New("still broken"), "poison"))
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
		// outstanding, so its attempts are preserved. Removing and re-adding is
		// how an operator restarts one, and it is deliberately not automatic.
		must.NoError(t, q.EnqueueKeys(t.Context(), "poison"))

		items, err = q.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		test.SliceEmpty(t, items)
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

					must.NoError(t, q.Complete(context.Background(), claimedKeys(claimed)...))
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

			must.NoError(t, q.Complete(t.Context(), key))
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

		must.NoError(t, q.Complete(t.Context(), "waiting"))

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

// The claim's LIMIT sits above the lock, so a row another transaction holds is
// skipped and replaced rather than counted against the batch. That is what lets
// a fleet of claimers each get full batches instead of dividing one, and it is a
// property of the statement's shape rather than of SKIP LOCKED itself — pushing
// the LIMIT into a subquery beneath the lock would still be correct and would
// quietly halve throughput under contention.
//
// It needs a real server and a second connection holding locks open, which is
// why it lives here and not beside the other claim tests.
func TestWorkQueue_ClaimFillsItsBatchAroundLockedRows(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		createTable(T, client, "contention")

		q, err := New[string](ctx, &Config{Name: "contended", TablePrefix: "contention"}, client)
		must.NoError(T, err)
		T.Cleanup(func() { _ = q.Close(context.WithoutCancel(ctx)) })

		keys := make([]string, 0, 10)
		for i := range 10 {
			keys = append(keys, fmt.Sprintf("k%02d", i))
		}

		must.NoError(T, q.EnqueueKeys(ctx, keys...))

		// A competing claimer, frozen mid-claim: three rows locked and not yet
		// released.
		tx, err := client.WriteDB().BeginTx(ctx, nil)
		must.NoError(T, err)
		T.Cleanup(func() { _ = tx.Rollback() })

		rows, err := tx.QueryContext(ctx,
			"SELECT item_key FROM contention_work_queue_items WHERE queue_name = $1 "+
				"ORDER BY item_key LIMIT 3 FOR UPDATE SKIP LOCKED", "contended")
		must.NoError(T, err)

		locked := 0
		for rows.Next() {
			locked++
		}

		must.NoError(T, rows.Err())
		must.NoError(T, rows.Close())
		must.EqOp(T, 3, locked)

		// Five asked for, three unavailable, seven left to choose from: a full
		// five come back.
		claimed, err := q.Claim(ctx, 5, time.Minute)
		must.NoError(T, err)
		test.SliceLen(T, 5, claimed)

		// And none of them is a row the other transaction is holding, which is
		// SKIP LOCKED doing its half of the job.
		for i := range claimed {
			test.SliceNotContains(T, []string{"k00", "k01", "k02"}, claimed[i].Key)
		}
	})
}

// Struct keys are the shape the extraction came from — a composite identifier,
// not a string — so the JSON codec is exercised against a real column rather
// than only in memory.
func TestWorkQueue_StructKeys(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		createTable(T, client, "structkeys")

		q, err := New[pairKey](ctx, &Config{Name: "pairs", TablePrefix: "structkeys"}, client)
		must.NoError(T, err)
		T.Cleanup(func() { _ = q.Close(context.WithoutCancel(ctx)) })

		want := []pairKey{
			{Profile: "car", Origin: 1, Dest: 2},
			{Profile: "bike", Origin: 1, Dest: 2},
			{Profile: "car", Origin: 2, Dest: 1},
		}

		entries := make([]Entry[pairKey], 0, len(want))
		for _, key := range want {
			entries = append(entries, Entry[pairKey]{Key: key})
		}

		must.NoError(T, q.Enqueue(ctx, entries...))

		items, err := q.Claim(ctx, 10, time.Minute)
		must.NoError(T, err)
		must.SliceLen(T, len(want), items)

		got := make([]pairKey, 0, len(items))
		for i := range items {
			got = append(got, items[i].Key)
		}

		for _, key := range want {
			test.SliceContains(T, got, key)
		}

		must.NoError(T, q.Complete(ctx, want...))

		stats, err := q.Stats(ctx)
		must.NoError(T, err)
		test.EqOp(T, int64(0), stats.Pending)
	})
}

// TestMigrations_RealServer proves the shipped DDL is accepted verbatim, and
// that re-running it is a no-op — the property every statement's IF NOT EXISTS
// is there for, and the one a consumer's migration runner depends on.
func TestMigrations_RealServer(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		stmts, err := migrations.Statements(dialect.Postgres, "ddl_check")
		must.NoError(T, err)

		for range 2 {
			for _, stmt := range stmts {
				_, execErr := pg.DB.ExecContext(ctx, stmt)
				must.NoError(T, execErr, must.Sprintf("executing %q", stmt))
			}
		}

		for _, stmt := range stmts {
			test.False(T, strings.Contains(stmt, "{{"))
		}
	})
}
