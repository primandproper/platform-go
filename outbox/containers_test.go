package outbox

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/outbox/internal/outboxdb"
	"github.com/primandproper/platform-go/v14/outbox/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// defaultMySQLImage pins the MariaDB flavor this suite exercises; mysqltest's
// default is stock MySQL.
const defaultMySQLImage = "mariadb:11"

// suitePoolSize is how many connections the suite's one client may open.
//
// It has to exceed one, and the reason is the whole of what the concurrency
// subtests below can see. A pool of one hands the second transaction's BEGIN a
// connection only once the first has committed, so two relays can never be in
// their claim transactions at the same time and a claim that leases rows
// another relay already holds looks correct. The subtests still assert the
// property rather than the pool; this is what gives them a chance to observe
// it being violated.
const suitePoolSize = 8

// tableCounter names a fresh table per subtest. Subtests share one container,
// so they must not share a table — the claim predicate is global to the table
// and one test's backlog would be another's.
var tableCounter atomic.Uint64

// dialectEnv is one live database plus the dialect and claim mode the suite
// should exercise against it.
type dialectEnv struct {
	client    database.Client
	dialect   dialect.Dialect
	claimMode ClaimMode
}

// newTable creates a uniquely namespaced outbox table and returns the
// namespace. The resolved table name is tableFor(namespace); raw SQL below goes
// through it rather than concatenating by hand, so the harness and the schema
// cannot disagree about the component segment.
func (e *dialectEnv) newTable(t *testing.T) string {
	t.Helper()

	name := fmt.Sprintf("ns%d", tableCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, name)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return name
}

// writer builds a Writer bound to the supplied table.
func (e *dialectEnv) writer(t *testing.T, c *stubClock, table string) *Writer {
	t.Helper()

	w, err := NewWriter(e.dialect, WithWriterClock(c), WithWriterTablePrefix(table))
	must.NoError(t, err)

	return w
}

// relay builds a Relay bound to the supplied table.
func (e *dialectEnv) relay(t *testing.T, c *stubClock, table string) (*Relay, *recordingPublisher) {
	t.Helper()

	return newTestRelay(t, e.client, c, func(cfg *RelayConfig) {
		cfg.ClaimMode = e.claimMode
		cfg.TablePrefix = table
	})
}

// countIn is countRows against an explicitly named table.
func countIn(t *testing.T, client database.Client, table, where string) int {
	t.Helper()

	var n int
	must.NoError(t, client.Reader().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+tableFor(table)+" WHERE "+where).
		Scan(&n))

	return n
}

// runDialectSuite is the behavioral contract every dialect owes. SQLite is
// covered by the in-process tests; this exists so the SQL that only a real
// server can validate — numbered placeholders, SKIP LOCKED, the correlated
// ordering subquery, MySQL's capped DELETE, native boolean and
// timestamp handling — is actually executed rather than merely rendered.
func runDialectSuite(t *testing.T, env *dialectEnv) {
	t.Helper()

	t.Run("publishes committed messages", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)
		relay, rec := env.relay(t, c, table)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return w.Enqueue(t.Context(), q,
				Message{Topic: "orders", Payload: map[string]any{"id": "a"}},
				Message{Topic: "orders", Payload: map[string]any{"id": "b"}},
			)
		}))

		relay.cycle(t.Context())

		test.SliceLen(t, 2, rec.payloads())
		test.EqOp(t, 0, countIn(t, env.client, table, "published_at IS NULL"))
	})

	t.Run("rolls back with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)

		boom := platformerrors.New("caller work failed")

		err := env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			if enqueueErr := w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "a"}}); enqueueErr != nil {
				return enqueueErr
			}

			return boom
		})
		test.ErrorIs(t, err, boom)

		test.EqOp(t, 0, countIn(t, env.client, table, "1=1"))
	})

	t.Run("reschedules a failed publish and retries after the backoff", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)
		relay, rec := env.relay(t, c, table)

		rec.fail(platformerrors.New("broker down"))

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		}))

		relay.cycle(t.Context())

		test.SliceEmpty(t, rec.payloads())
		test.EqOp(t, 1, countIn(t, env.client, table, "published_at IS NULL AND attempts = 1"))

		// Still inside the backoff window.
		relay.cycle(t.Context())
		test.EqOp(t, 1, countIn(t, env.client, table, "attempts = 1"))

		rec.fail(nil)
		c.advance(time.Minute)

		relay.cycle(t.Context())
		test.SliceLen(t, 1, rec.payloads())
		test.EqOp(t, 0, countIn(t, env.client, table, "published_at IS NULL"))
	})

	t.Run("quarantines a poison message without blocking the queue", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)
		relay, rec := env.relay(t, c, table)

		rec.fail(platformerrors.New("poison"))

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		}))

		for range 3 {
			relay.cycle(t.Context())
			c.advance(time.Hour)
		}

		// Native boolean handling differs per dialect; this is the assertion
		// that catches a TINYINT(1) mismatch.
		test.EqOp(t, 1, countIn(t, env.client, table, "quarantined = TRUE"))

		rec.fail(nil)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "b"}})
		}))

		relay.cycle(t.Context())

		test.SliceLen(t, 1, rec.payloads())
		test.EqOp(t, 1, countIn(t, env.client, table, "quarantined = TRUE"))
	})

	t.Run("holds a lease against a second claim", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)
		relay, _ := env.relay(t, c, table)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		}))

		claimed, err := relay.claim(t.Context())
		must.NoError(t, err)
		test.SliceLen(t, 1, claimed)

		again, err := relay.claim(t.Context())
		must.NoError(t, err)
		test.SliceEmpty(t, again)

		c.advance(DefaultLeaseDuration + time.Second)

		reclaimed, err := relay.claim(t.Context())
		must.NoError(t, err)
		test.SliceLen(t, 1, reclaimed)
	})

	t.Run("claims at most one message per partition key", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)
		relay, rec := env.relay(t, c, table)

		// This is the correlated NOT EXISTS subquery under a real planner.
		for _, id := range []string{"first", "second", "third"} {
			must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
				return w.Enqueue(t.Context(), q, Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": id}})
			}))
			c.advance(time.Millisecond)
		}

		for range 3 {
			relay.cycle(t.Context())
		}

		test.Eq(t, []string{`{"id":"first"}`, `{"id":"second"}`, `{"id":"third"}`}, rec.payloads())
	})

	// The case above advances the clock between enqueues, so created_at alone
	// separates the rows. Here one Enqueue stamps all three with a single
	// timestamp and only the id tiebreak orders them — which puts this server's
	// collation on the hook, since the comparison is over xid text.
	t.Run("orders a same-timestamp batch for one partition key", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)
		relay, rec := env.relay(t, c, table)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return w.Enqueue(t.Context(), q,
				Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "first"}},
				Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "second"}},
				Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "third"}},
			)
		}))

		// Exactly one publish per cycle: the successor is not claimable until its
		// predecessor is marked published, even though they share a created_at.
		for want := 1; want <= 3; want++ {
			relay.cycle(t.Context())
			test.SliceLen(t, want, rec.payloads())
		}

		test.Eq(t, []string{`{"id":"first"}`, `{"id":"second"}`, `{"id":"third"}`}, rec.payloads())
	})

	// Two relays selecting at the same instant must come away with disjoint
	// batches, and this is the assertion only a real server can make: two
	// transactions genuinely overlapping, rather than two statements in one.
	//
	// It runs under both modes, because both owe it. SKIP LOCKED divides the
	// backlog at the select, by locking each batch as it is read; the lease
	// mode divides it at the claim, whose guarded UPDATE hands a contested
	// batch to one relay and leaves the other holding ids it did not win — and
	// whose read-back therefore asks for its own claim rather than for those
	// ids. Before that pair, a lease-mode fleet published every row twice.
	t.Run("concurrent relays divide the backlog", func(t *testing.T) {
		t.Parallel()

		const (
			messages  = 24
			batchSize = 4
		)

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)

		// A batch smaller than the backlog is what forces several cycles each,
		// so the two relays are actually selecting at once rather than one
		// draining the table before the other starts.
		divided := func(cfg *RelayConfig) {
			cfg.ClaimMode = env.claimMode
			cfg.TablePrefix = table
			cfg.BatchSize = batchSize
		}

		first, firstRec := newTestRelay(t, env.client, c, divided)
		second, secondRec := newTestRelay(t, env.client, c, divided)

		for i := range messages {
			must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
				return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": i}})
			}))
		}

		var wg sync.WaitGroup

		// One cycle per message each, rather than one per batch: a relay that
		// loses a contested claim in the lease mode publishes nothing that
		// cycle, so the fleet needs more cycles than the backlog divides
		// into. The surplus ones run against a drained table and cost a
		// select that returns nothing.
		for _, relay := range []*Relay{first, second} {
			wg.Go(func() {
				for range messages {
					relay.cycle(t.Context())
				}
			})
		}

		wg.Wait()

		// Every message published, and none of them twice: the two counts are
		// the two halves of "disjoint", and neither alone would catch a claim
		// that stopped dividing. A fleet that double-claims still drains the
		// table, so the depth reads clean and only the publish count says so.
		test.EqOp(t, 0, countIn(t, env.client, table, "published_at IS NULL"))
		test.SliceLen(t, messages, append(firstRec.payloads(), secondRec.payloads()...))
	})

	// The interleaving the guard exists for, held open on purpose.
	//
	// A relay's own cycle can never be caught between its select and its claim
	// — they run inside one transaction — so the state that breaks a
	// lease-mode fleet is two transactions whose selects both land before
	// either claim writes. That is the ordinary interleaving of two relays
	// polling the same table, and it is rare enough per cycle that a suite
	// racing two relays freely can pass through luck. This one arranges it:
	// both selects complete, and only then do the claims run.
	//
	// Addressed by id alone, as the claim once was, both transactions take
	// every row and both read every row back. What must happen instead is that
	// each row is taken by exactly one of them.
	t.Run("overlapping claims take disjoint rows", func(t *testing.T) {
		t.Parallel()

		const (
			messages  = 8
			batchSize = 4
		)

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)

		relay, _ := newTestRelay(t, env.client, c, func(cfg *RelayConfig) {
			cfg.ClaimMode = env.claimMode
			cfg.TablePrefix = table
			cfg.BatchSize = batchSize
		})

		for i := range messages {
			must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
				return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": i}})
			}))
		}

		now := c.Now().UTC()
		leaseUntil := now.Add(DefaultLeaseDuration)

		var (
			// selected is the barrier. Both transactions hold whatever their
			// select gave them until the other has one too.
			selected sync.WaitGroup
			claims   sync.WaitGroup

			mu    sync.Mutex
			taken = map[string]int{}

			// Collected rather than asserted in the goroutine: a failed
			// assertion from one is not the test's own goroutine to fail.
			failures [2]error
		)

		selected.Add(2)

		for i := range 2 {
			claims.Go(func() {
				failures[i] = env.client.WithTransaction(t.Context(), func(q database.Tx) error {
					ids, err := relay.selectClaimable(t.Context(), q, now)
					if err != nil {
						return platformerrors.Wrap(err, "selecting")
					}

					selected.Done()
					selected.Wait()

					// A fresh name per claim, which is what the relay mints.
					token := identifiers.New()

					if err = relay.q.ClaimOutboxMessages(t.Context(), q, outboxdb.ClaimOutboxMessagesParams{
						ClaimedUntil:   &leaseUntil,
						ClaimedBy:      &token,
						Now:            now,
						LeaseExpiredBy: &now,
						IDs:            ids,
					}); err != nil {
						return platformerrors.Wrap(err, "claiming")
					}

					rows, err := relay.q.FetchClaimedOutboxMessages(t.Context(), q, outboxdb.FetchClaimedOutboxMessagesParams{
						ClaimedBy: &token,
						IDs:       ids,
					})
					if err != nil {
						return platformerrors.Wrap(err, "reading back")
					}

					mu.Lock()
					defer mu.Unlock()

					for j := range rows {
						taken[rows[j].ID]++
					}

					return nil
				})
			})
		}

		claims.Wait()

		// At most one of the two may be refused, and a refusal is one of the
		// two shapes a server answers a contested claim in. Postgres
		// re-evaluates the guard once the row lock clears, so the loser's
		// UPDATE simply matches nothing; MariaDB refuses the transaction
		// outright — "record has changed since last read" — which the relay
		// reports as a failed claim and its next cycle retries. Both leave the
		// rows to the winner, which is the property; which one a server picks
		// is the server's business.
		refused := 0

		for i := range failures {
			if failures[i] != nil {
				refused++
			}
		}

		must.LessEq(t, 1, refused, must.Sprintf("both claims refused: %v; %v", failures[0], failures[1]))

		// Something was claimed — a pair of empty batches would satisfy
		// disjointness and prove nothing.
		test.MapNotEmpty(t, taken)

		for id, n := range taken {
			test.EqOp(t, 1, n, test.Sprintf("message %q was taken %d times", id, n))
		}
	})

	// The same interleaving, against the other thing that can happen to a row
	// between a select and the claim that follows it: not another claim, but
	// the retirement of the row itself.
	//
	// A relay publishing a batch clears the lease as it stamps published_at —
	// the claim is over, so the name and the horizon come off. That leaves a
	// retired row looking exactly like a free one to anything testing the
	// lease alone, and the claim's guard tested the lease alone. A relay whose
	// select landed before the winner's claim, and whose own claim lands after
	// the winner's retirement, therefore took rows that had already been
	// published and published them a second time.
	//
	// So the guard owes the select's whole test and not just the half about
	// leases: what disqualifies a row at selection time disqualifies it at
	// claim time, because every one of those columns can be written by
	// somebody else in between. Held open here rather than raced, because the
	// window is a few milliseconds wide and a suite that races for it passes
	// by luck four times in five.
	t.Run("a claim does not take rows that were published while it waited", func(t *testing.T) {
		t.Parallel()

		const (
			messages  = 4
			batchSize = 4
		)

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)

		// The winner runs a whole cycle; the straggler is driven statement by
		// statement, so the interleaving is arranged rather than hoped for.
		winner, winnerRec := newTestRelay(t, env.client, c, func(cfg *RelayConfig) {
			cfg.ClaimMode = env.claimMode
			cfg.TablePrefix = table
			cfg.BatchSize = batchSize
		})

		straggler, _ := newTestRelay(t, env.client, c, func(cfg *RelayConfig) {
			cfg.ClaimMode = env.claimMode
			cfg.TablePrefix = table
			cfg.BatchSize = batchSize
		})

		for i := range messages {
			must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
				return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": i}})
			}))
		}

		now := c.Now().UTC()
		leaseUntil := now.Add(DefaultLeaseDuration)

		token := identifiers.New()

		// The straggler's transaction is allowed to be refused outright, which
		// is the other shape a server answers a contested write in: Postgres
		// re-evaluates the guard once the row lock clears and matches nothing,
		// MariaDB reports "record has changed since last read" and rolls the
		// whole thing back. A refusal claims nothing, so it satisfies what is
		// being asserted here just as an empty match does; the assertions below
		// are over the rows either way, and they are what decides.
		claimErr := env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			// The straggler's select, taken before anybody has claimed
			// anything. Under SKIP LOCKED this holds the rows and the winner
			// below simply finds none; under the lease mode it holds nothing
			// and the winner takes them all. Both are fine — what neither may
			// do is let the claim at the bottom have rows that got published.
			ids, err := straggler.selectClaimable(t.Context(), q, now)
			if err != nil {
				return platformerrors.Wrap(err, "selecting")
			}

			// The winner's entire cycle, on its own connection: it claims,
			// publishes, and retires the batch — which is the write that nulls
			// the lease out from under the ids the straggler is holding.
			winner.cycle(t.Context())

			if err = straggler.q.ClaimOutboxMessages(t.Context(), q, outboxdb.ClaimOutboxMessagesParams{
				ClaimedUntil:   &leaseUntil,
				ClaimedBy:      &token,
				Now:            now,
				LeaseExpiredBy: &now,
				IDs:            ids,
			}); err != nil {
				return platformerrors.Wrap(err, "claiming")
			}

			if _, err = straggler.q.FetchClaimedOutboxMessages(t.Context(), q, outboxdb.FetchClaimedOutboxMessagesParams{
				ClaimedBy: &token,
				IDs:       ids,
			}); err != nil {
				return platformerrors.Wrap(err, "reading back")
			}

			return nil
		})
		if claimErr != nil {
			t.Logf("the straggler's claim was refused rather than emptied: %v", claimErr)
		}

		// The invariant, and it is the same sentence in both modes: nothing the
		// straggler came away holding may already have been published.
		//
		// What that permits differs, which is why it is phrased about the rows
		// rather than about a count. Under SKIP LOCKED the straggler's select
		// took the locks, so the winner published nothing and the straggler
		// holds all four legitimately — still unpublished, still its to send.
		// Under the lease mode the winner published all four, so the straggler
		// must hold none. A row that is both published and claimed here is the
		// bug: a retired row handed back out because clearing its lease made it
		// look free.
		test.EqOp(t, 0, countIn(t, env.client, table,
			"claimed_by = '"+token+"' AND published_at IS NOT NULL"))

		// And whatever the winner did send, it sent once.
		test.SliceLen(t, len(uniqueStrings(winnerRec.payloads())), winnerRec.payloads())
	})

	// The other end of the same name, on a real server: what a relay that
	// overran its lease is allowed to write once it comes back.
	//
	// The in-process suite pins this against SQLite, where the claim and the two
	// outcome writes are the same text. It runs here because the guard is the
	// one predicate in this corpus whose loser is decided by a server rather
	// than by the statement: the straggler's UPDATE has to re-read a
	// committed claimed_by written by a different transaction.
	t.Run("a straggler cannot retire or fail the rows it lost", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)
		relay, _ := env.relay(t, c, table)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		}))

		straggler, err := relay.claim(t.Context())
		must.NoError(t, err)
		must.SliceLen(t, 1, straggler)

		c.advance(DefaultLeaseDuration + time.Second)

		holder, err := relay.claim(t.Context())
		must.NoError(t, err)
		must.SliceLen(t, 1, holder)
		must.NotEqOp(t, straggler[0].claimToken, holder[0].claimToken)

		must.NoError(t, relay.markPublished(t.Context(), straggler[0].claimToken, []string{straggler[0].id}))

		straggler[0].attempts = int(relay.cfg.Backoff.MaxAttempts)
		relay.recordFailure(t.Context(), &straggler[0], platformerrors.New("broker refused"))

		// Untouched on every count: not retired, not quarantined, and still
		// leased to the relay that is publishing it.
		test.EqOp(t, 1, countIn(t, env.client, table, "published_at IS NULL"))
		test.EqOp(t, 0, countIn(t, env.client, table, "quarantined = TRUE"))
		test.EqOp(t, 1, countIn(t, env.client, table, "claimed_by IS NOT NULL"))

		// The holder's own retirement lands, so what the guard refused was the
		// straggler and not the statement.
		must.NoError(t, relay.markPublished(t.Context(), holder[0].claimToken, []string{holder[0].id}))
		test.EqOp(t, 0, countIn(t, env.client, table, "published_at IS NULL"))
	})

	t.Run("reports backlog depth and age", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)
		relay, _ := env.relay(t, c, table)

		depth, age, err := relay.backlog(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), depth)
		test.EqOp(t, time.Duration(0), age)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		}))

		c.advance(90 * time.Second)

		// The oldest instant is the one value this package reads back as a
		// time, and every driver renders a timestamp column differently —
		// which is why this runs per dialect rather than on SQLite alone.
		depth, age, err = relay.backlog(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), depth)
		test.EqOp(t, 90*time.Second, age)

		relay.cycle(t.Context())

		depth, age, err = relay.backlog(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), depth)
		test.EqOp(t, time.Duration(0), age)
	})

	t.Run("reaps published rows past retention", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		table := env.newTable(t)
		w := env.writer(t, c, table)
		relay, _ := env.relay(t, c, table)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		}))

		relay.cycle(t.Context())
		test.EqOp(t, 1, countIn(t, env.client, table, "published_at IS NOT NULL"))

		relay.reap(t.Context())
		test.EqOp(t, 1, countIn(t, env.client, table, "1=1"))

		c.advance(DefaultRetention + time.Hour)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "b"}})
		}))

		// Three grammars for one capped delete: MySQL bounds the DELETE
		// itself, and the other two name the doomed rows through a subquery
		// over the table being deleted from. See querygen's bounded prune.
		relay.reap(t.Context())

		test.EqOp(t, 0, countIn(t, env.client, table, "published_at IS NOT NULL"))
		test.EqOp(t, 1, countIn(t, env.client, table, "published_at IS NULL"))
	})
}

func TestOutbox_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{
			connectionString: pg.ConnectionString,
			maxOpenConns:     suitePoolSize,
		})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		// Both claim modes: SKIP LOCKED is the path that only a real server
		// can validate, and ClaimLease is the one whose exclusivity is the
		// claim's own rather than the select's.
		for _, mode := range []ClaimMode{ClaimSkipLocked, ClaimLease} {
			T.Run(string(mode), func(t *testing.T) {
				t.Parallel()

				runDialectSuite(t, &dialectEnv{
					dialect:   dialect.Postgres,
					claimMode: mode,
					client:    client,
				})
			})
		}
	}, pgtest.WithMaxOpenConns(32))
}

// runWithMySQL boots a MySQL container via mysqltest and hands its closure a
// database.Client against it.
func runWithMySQL(tb testing.TB, fn func(ctx context.Context, client database.Client)) {
	tb.Helper()

	mysqltest.Run(tb, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{
			connectionString: my.ConnectionString,
			maxOpenConns:     suitePoolSize,
		})
		must.NoError(tb, err)
		tb.Cleanup(func() { _ = client.Close() })

		fn(ctx, client)
	},
		mysqltest.WithImage(defaultMySQLImage),
		mysqltest.WithCredentials("outboxtest", "outboxtest", "outboxtest"),
	)
}

func TestOutbox_MySQL(T *testing.T) {
	T.Parallel()

	runWithMySQL(T, func(_ context.Context, client database.Client) {
		for _, mode := range []ClaimMode{ClaimSkipLocked, ClaimLease} {
			T.Run(string(mode), func(t *testing.T) {
				t.Parallel()

				runDialectSuite(t, &dialectEnv{
					dialect:   dialect.MySQL,
					claimMode: mode,
					client:    client,
				})
			})
		}
	})
}

// TestOutbox_MigratorIntegration_Containers is the end-to-end wiring a consumer
// actually writes: hand migrations.SQL to database/migrate as a generated
// migration, let the normal migration run create the table, then use it. It runs
// against real servers because the point is that the generated migration
// survives goose's own statement splitting on each dialect.
func TestOutbox_MigratorIntegration_Containers(T *testing.T) {
	T.Parallel()

	assertUsable := func(t *testing.T, client database.Client, d dialect.Dialect, table string) {
		t.Helper()

		c := newStubClock()

		w, err := NewWriter(d, WithWriterClock(c), WithWriterTablePrefix(table))
		must.NoError(t, err)

		relay, rec := newTestRelay(t, client, c, func(cfg *RelayConfig) {
			cfg.ClaimMode = ClaimSkipLocked
			cfg.TablePrefix = table
		})

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
			return w.Enqueue(t.Context(), q, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		}))

		relay.cycle(t.Context())

		test.Eq(t, []string{`{"id":"a"}`}, rec.payloads())
	}

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(_ context.Context, pg *pgtest.Instance) {
			client, err := postgres.NewDatabaseClient(t.Context(), &testClientConfig{connectionString: pg.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			const table = "migrated_outbox"

			migrateOutboxTable(t, client, dialect.Postgres, table)
			assertUsable(t, client, dialect.Postgres, table)
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(_ context.Context, client database.Client) {
			const table = "migrated_outbox"

			migrateOutboxTable(t, client, dialect.MySQL, table)
			assertUsable(t, client, dialect.MySQL, table)
		})
	})
}

// TestMigrations_RealServers proves the shipped DDL is accepted verbatim by
// each server, independent of whether the relay then exercises every column.
func TestMigrations_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(ctx context.Context, pg *pgtest.Instance) {
			stmts, err := migrations.Statements(dialect.Postgres, "ddl_check")
			must.NoError(t, err)

			for _, stmt := range stmts {
				_, execErr := pg.DB.ExecContext(ctx, stmt)
				must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
			}

			// Re-running must be a no-op: every statement is IF NOT EXISTS.
			for _, stmt := range stmts {
				_, execErr := pg.DB.ExecContext(ctx, stmt)
				must.NoError(t, execErr, must.Sprintf("re-executing %q", stmt))
			}
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(ctx context.Context, client database.Client) {
			stmts, err := migrations.Statements(dialect.MySQL, "ddl_check")
			must.NoError(t, err)

			// Executed twice: CREATE TABLE IF NOT EXISTS carries the inline KEY
			// clauses with it, so a second run must not trip over them.
			for range 2 {
				for _, stmt := range stmts {
					_, execErr := client.Writer().ExecContext(ctx, stmt)
					must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
				}
			}
		})
	})

	T.Run("statements carry no unrendered placeholder", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{
			dialect.Postgres, dialect.MySQL, dialect.SQLite,
		} {
			stmts, err := migrations.Statements(d, "ddl_check")
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.False(t, strings.Contains(stmt, "{{"))
			}
		}
	})
}

// uniqueStrings is the distinct set of s, preserving nothing but membership.
func uniqueStrings(s []string) []string {
	seen := make(map[string]struct{}, len(s))
	out := make([]string, 0, len(s))

	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}

		seen[v] = struct{}{}
		out = append(out, v)
	}

	return out
}
