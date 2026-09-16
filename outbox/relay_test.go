package outbox

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/outbox/internal/outboxdb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/messagequeue"
	messagequeuemock "github.com/primandproper/primitives-go/v2/messagequeue/mock"
	retrycfg "github.com/primandproper/primitives-go/v2/retry/config"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingPublisher captures what reached the broker and can be told to fail.
type recordingPublisher struct {
	err      error
	received [][]byte
	mu       sync.Mutex
}

func (p *recordingPublisher) Publish(_ context.Context, data any, _ ...messagequeue.PublishOption) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.err != nil {
		return p.err
	}

	raw, ok := data.(json.RawMessage)
	if !ok {
		return platformerrors.Newf("expected json.RawMessage, got %T", data)
	}

	p.received = append(p.received, append([]byte(nil), raw...))

	return nil
}

func (p *recordingPublisher) payloads() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]string, 0, len(p.received))
	for _, r := range p.received {
		out = append(out, string(r))
	}

	return out
}

func (p *recordingPublisher) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.err = err
}

// newTestRelay wires a relay against the SQLite harness and a single recording
// publisher shared by every topic.
func newTestRelay(t *testing.T, client database.Client, c *stubClock, opts ...func(*RelayConfig)) (*Relay, *recordingPublisher) {
	t.Helper()

	rec := &recordingPublisher{}

	publisher := &messagequeuemock.PublisherMock{
		PublishFunc: rec.Publish,
		StopFunc:    func() {},
	}

	provider := &messagequeuemock.PublisherProviderMock{
		NewPublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
			return publisher, nil
		},
		CloseFunc: func() {},
	}

	cfg := &RelayConfig{
		ClaimMode: ClaimLease,
		Backoff: retrycfg.Config{
			MaxAttempts:  3,
			InitialDelay: time.Second,
			MaxDelay:     time.Minute,
			Multiplier:   2,
		},
	}
	for _, opt := range opts {
		opt(cfg)
	}

	relay, err := NewRelay(t.Context(), cfg, client, provider, WithRelayClock(c))
	must.NoError(t, err)

	return relay, rec
}

func newTestWriter(t *testing.T, c *stubClock) *Writer {
	t.Helper()

	w, err := NewWriter(dialect.SQLite, WithWriterClock(c))
	must.NoError(t, err)

	return w
}

func TestRelay_cycle(T *testing.T) {
	T.Parallel()

	T.Run("publishes committed messages", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)

		enqueue(t, client, newTestWriter(t, c),
			Message{Topic: "orders", Payload: map[string]any{"id": "a"}},
			Message{Topic: "orders", Payload: map[string]any{"id": "b"}},
		)

		relay.cycle(t.Context())

		// The broker receives exactly what a direct Publish of the same value
		// would have sent — no envelope, no double encoding.
		test.Eq(t, []string{`{"id":"a"}`, `{"id":"b"}`}, rec.payloads())
		test.EqOp(t, 0, countRows(t, client, "published_at IS NULL"))
	})

	T.Run("claims nothing when the outbox is empty", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)

		relay.cycle(t.Context())

		test.SliceEmpty(t, rec.payloads())
	})

	T.Run("reschedules a failed publish without publishing it", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)

		rec.fail(platformerrors.New("broker down"))

		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "a"}})

		relay.cycle(t.Context())

		test.SliceEmpty(t, rec.payloads())
		test.EqOp(t, 1, countRows(t, client, "published_at IS NULL AND attempts = 1"))
		test.EqOp(t, 0, countRows(t, client, "quarantined = TRUE"))

		// Still backing off: the next cycle must not pick it up again.
		relay.cycle(t.Context())
		test.EqOp(t, 1, countRows(t, client, "attempts = 1"))
	})

	T.Run("retries once the backoff has elapsed", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)

		rec.fail(platformerrors.New("broker down"))
		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "a"}})

		relay.cycle(t.Context())
		test.EqOp(t, 1, countRows(t, client, "attempts = 1"))

		rec.fail(nil)
		c.advance(time.Minute)

		relay.cycle(t.Context())
		test.Eq(t, []string{`{"id":"a"}`}, rec.payloads())
		test.EqOp(t, 0, countRows(t, client, "published_at IS NULL"))
	})

	T.Run("quarantines a message that exhausts its attempts", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)

		rec.fail(platformerrors.New("poison"))
		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "a"}})

		// MaxAttempts is 3; each cycle consumes one attempt.
		for range 3 {
			relay.cycle(t.Context())
			c.advance(time.Hour)
		}

		test.EqOp(t, 1, countRows(t, client, "quarantined = TRUE"))

		// A quarantined message never blocks the queue behind it.
		rec.fail(nil)
		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "b"}})

		relay.cycle(t.Context())

		test.Eq(t, []string{`{"id":"b"}`}, rec.payloads())
	})

	T.Run("does not reclaim a leased message before its lease expires", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, c)

		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "a"}})

		claimed, err := relay.claim(t.Context())
		must.NoError(t, err)
		test.SliceLen(t, 1, claimed)

		// A second relay must see nothing while the lease is held.
		again, err := relay.claim(t.Context())
		must.NoError(t, err)
		test.SliceEmpty(t, again)

		c.advance(DefaultLeaseDuration + time.Second)

		reclaimed, err := relay.claim(t.Context())
		must.NoError(t, err)
		test.SliceLen(t, 1, reclaimed)
	})
}

func TestRelay_ordering(T *testing.T) {
	T.Parallel()

	T.Run("claims at most one message per partition key", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		// Three messages on one key, enqueued in order. Distinct created_at
		// values, since the ordering predicate compares them.
		for _, id := range []string{"first", "second", "third"} {
			enqueue(t, client, w, Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": id}})
			c.advance(time.Millisecond)
		}

		// Each cycle may only take the oldest unpublished message for the key,
		// so order survives even with concurrent relays.
		for range 3 {
			relay.cycle(t.Context())
		}

		test.Eq(t, []string{`{"id":"first"}`, `{"id":"second"}`, `{"id":"third"}`}, rec.payloads())
	})

	T.Run("claims unkeyed messages together", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		for _, id := range []string{"a", "b", "c"} {
			enqueue(t, client, w, Message{Topic: "orders", Payload: map[string]any{"id": id}})
			c.advance(time.Millisecond)
		}

		relay.cycle(t.Context())

		test.SliceLen(t, 3, rec.payloads())
	})

	T.Run("claims distinct keys together", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		enqueue(t, client, w,
			Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "a"}},
			Message{Topic: "orders", Key: "cart-2", Payload: map[string]any{"id": "b"}},
		)

		relay.cycle(t.Context())

		test.SliceLen(t, 2, rec.payloads())
	})

	// The cases above advance the clock between enqueues, so created_at alone
	// separates the rows. These two do not: one Enqueue stamps every row with a
	// single timestamp, which is the case where a created_at-only predicate
	// silently stops ordering anything.
	T.Run("claims one message per key when a batch shares a timestamp", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		enqueue(t, client, w,
			Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "first"}},
			Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "second"}},
			Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "third"}},
		)

		claimed, err := relay.claim(t.Context())
		must.NoError(t, err)

		test.SliceLen(t, 1, claimed)
	})

	T.Run("publishes a same-timestamp batch one message per cycle, in order", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		enqueue(t, client, w,
			Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "first"}},
			Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "second"}},
			Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "third"}},
		)

		// The successor becomes claimable only once its predecessor is marked
		// published, so the batch drains one per cycle rather than all at once.
		for want := 1; want <= 3; want++ {
			relay.cycle(t.Context())
			test.SliceLen(t, want, rec.payloads())
		}

		test.Eq(t, []string{`{"id":"first"}`, `{"id":"second"}`, `{"id":"third"}`}, rec.payloads())
	})

	T.Run("holds a key behind its failing predecessor within one Enqueue", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		enqueue(t, client, w,
			Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "first"}},
			Message{Topic: "orders", Key: "cart-1", Payload: map[string]any{"id": "second"}},
		)

		// The first message cannot be published. The second must not overtake
		// it: a consumer that sees "second" without "first" has observed the
		// key out of order, which is the one thing Key promises cannot happen.
		rec.fail(platformerrors.New("broker down"))
		relay.cycle(t.Context())
		test.SliceLen(t, 0, rec.payloads())

		// Once the predecessor lands, the successor follows — in order.
		rec.fail(nil)
		c.advance(time.Minute)
		relay.cycle(t.Context())
		relay.cycle(t.Context())

		test.Eq(t, []string{`{"id":"first"}`, `{"id":"second"}`}, rec.payloads())
	})
}

// TestRelay_claim_guardsTheLease is the double-claim, driven at the two
// statements it happens between.
//
// A relay's own claim() runs the select and the update in one transaction, so
// it can never be caught between them — which is exactly why the bug this
// pins was invisible from the outside. In the lease mode the select takes no
// row lock, so two relays ordinarily come away holding the same ids, and what
// decides which of them publishes is the guarded UPDATE and the read-back that
// asks for its name. Both are exercised here against the real SQL, with the
// selects already done and the two claims left to interleave.
func TestRelay_claim_guardsTheLease(T *testing.T) {
	T.Parallel()

	T.Run("a second claim of the same batch takes nothing", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		enqueue(t, client, w,
			Message{Topic: "orders", Payload: map[string]any{"id": "a"}},
			Message{Topic: "orders", Payload: map[string]any{"id": "b"}},
		)

		first, second := claimTwice(t, client, relay, c, allIDs)

		// The whole batch to the first claim and none of it to the second. A
		// read-back addressed by the ids alone would hand both relays both
		// rows, which is the double publish.
		test.SliceLen(t, 2, first)
		test.SliceEmpty(t, second)

		// The loser's UPDATE matched nothing, so it also spent nobody's
		// attempt: a message must not be one claim closer to quarantine for
		// having been asked about.
		test.EqOp(t, 2, countRows(t, client, "attempts = 1"))
	})

	T.Run("a claim takes the free part of a batch and reads back only that", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		enqueue(t, client, w,
			Message{Topic: "orders", Payload: map[string]any{"id": "a"}},
			Message{Topic: "orders", Payload: map[string]any{"id": "b"}},
		)

		// The first claim asks for the head of the batch; the second asks for
		// all of it, the way a relay with a larger batch size or a staler
		// select would. This is the case an affected-row count cannot answer:
		// the second claim won some of what it asked for, and only the name it
		// stamped says which.
		first, second := claimTwice(t, client, relay, c, func(ids []string) []string { return ids[:1] })

		test.SliceLen(t, 1, first)
		test.SliceLen(t, 1, second)

		// Disjoint, which is the point — the two claims name different rows.
		test.NotEqOp(t, first[0].ID, second[0].ID)
	})

	T.Run("an expired lease is claimable again", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		enqueue(t, client, w, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})

		claimed, err := relay.claim(t.Context())
		must.NoError(t, err)
		test.SliceLen(t, 1, claimed)

		// The guard is the select's own lease test repeated, so it has to lapse
		// on the same terms: a relay that died holding this row must not have
		// taken it out of circulation, and the new claim's name replaces the
		// dead one's.
		c.advance(DefaultLeaseDuration + time.Second)

		reclaimed, err := relay.claim(t.Context())
		must.NoError(t, err)
		test.SliceLen(t, 1, reclaimed)
		test.EqOp(t, 1, countRows(t, client, "attempts = 2"))
	})
}

// TestRelay_outcomeWrites_fenceOnTheClaim drives the interleaving from the far
// side of the publish.
//
// The claim guard divides the rows; this is what happens after. A relay slow
// enough to overrun its lease has had its rows taken by the time it comes back
// to say what became of them, and both of the writes it makes then — the
// retirement and the failure record — would otherwise land on somebody else's
// row. The lapse is arranged with the stub clock rather than raced for.
func TestRelay_outcomeWrites_fenceOnTheClaim(T *testing.T) {
	T.Parallel()

	// lapse takes one message through a claim, a lease expiry and a second
	// claim, and hands back both claims' view of it. The first is the straggler.
	lapse := func(t *testing.T) (client database.Client, relay *Relay, straggler, holder claimedMessage) {
		t.Helper()

		c := newStubClock()
		client = newTestClient(t)
		relay, _ = newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		enqueue(t, client, w, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})

		first, err := relay.claim(t.Context())
		must.NoError(t, err)
		must.SliceLen(t, 1, first)

		c.advance(DefaultLeaseDuration + time.Second)

		second, err := relay.claim(t.Context())
		must.NoError(t, err)
		must.SliceLen(t, 1, second)

		// Two claims of one row, and the names they stamped are different —
		// which is the whole of what the guards below compare.
		must.NotEqOp(t, first[0].claimToken, second[0].claimToken)

		return client, relay, first[0], second[0]
	}

	T.Run("a straggler's retirement retires nothing", func(t *testing.T) {
		t.Parallel()

		client, relay, straggler, holder := lapse(t)

		must.NoError(t, relay.markPublished(t.Context(), straggler.claimToken, []string{straggler.id}))

		// Still unpublished and still leased: the second relay is mid-publish,
		// and retiring its row here is the lost delivery the guard exists for.
		test.EqOp(t, 1, countRows(t, client, "published_at IS NULL"))
		test.EqOp(t, 1, countRows(t, client, "claimed_by IS NOT NULL"))

		// And the holder's own retirement still works, so the guard refuses the
		// straggler rather than the statement.
		must.NoError(t, relay.markPublished(t.Context(), holder.claimToken, []string{holder.id}))
		test.EqOp(t, 0, countRows(t, client, "published_at IS NULL"))
	})

	T.Run("a straggler's failure record records nothing", func(t *testing.T) {
		t.Parallel()

		client, relay, straggler, _ := lapse(t)

		// The straggler exhausted its attempts, so an unguarded write here
		// would not merely reschedule the holder's row — it would quarantine
		// a publish that is still in flight.
		straggler.attempts = int(relay.cfg.Backoff.MaxAttempts)

		relay.recordFailure(t.Context(), &straggler, platformerrors.New("broker refused"))

		test.EqOp(t, 0, countRows(t, client, "quarantined = TRUE"))
		test.EqOp(t, 0, countRows(t, client, "last_error IS NOT NULL"))

		// The holder's lease is intact: a released lease here would hand the
		// row to a third relay while the second one is still publishing it.
		test.EqOp(t, 1, countRows(t, client, "claimed_by IS NOT NULL"))
	})

	T.Run("a lapsed lease nobody else took still retires", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		enqueue(t, client, w, Message{Topic: "orders", Payload: map[string]any{"id": "a"}})

		claimed, err := relay.claim(t.Context())
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		// Past the horizon, and nobody reclaimed. The guard asks who holds the
		// row rather than whether the lease is fresh, so this relay still does
		// — and the work it actually did is recorded rather than repeated.
		c.advance(DefaultLeaseDuration + time.Second)

		must.NoError(t, relay.markPublished(t.Context(), claimed[0].claimToken, []string{claimed[0].id}))
		test.EqOp(t, 0, countRows(t, client, "published_at IS NULL"))
	})
}

// allIDs is the batch a claim asks for when it asks for everything its select
// returned, which is what a relay does.
func allIDs(ids []string) []string { return ids }

// claimTwice runs two claims over one select, the way two relays in the lease
// mode do, and returns what each of them read back.
//
// Both claims run on one transaction. That is not the interleaving in
// production — there it is two transactions and the engine's row lock that
// orders them — but it is the same pair of statements in the same order, and it
// is the half the guard is responsible for: a transaction sees its own writes,
// so the second claim tests the horizon the first one wrote. What a server does
// under two concurrent transactions is asserted against real servers in
// containers_test.go.
//
// asked narrows what the second claim requests, so a caller can arrange either
// the identical batch or a straddling one.
func claimTwice(
	t *testing.T,
	client database.Client,
	relay *Relay,
	c *stubClock,
	asked func([]string) []string,
) (first, second []outboxdb.FetchClaimedOutboxMessagesRow) {
	t.Helper()

	now := c.Now().UTC()
	leaseUntil := now.Add(DefaultLeaseDuration)

	must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
		ids, err := relay.selectClaimable(t.Context(), q, now)
		must.NoError(t, err)
		must.SliceNotEmpty(t, ids)

		claim := func(ids []string) []outboxdb.FetchClaimedOutboxMessagesRow {
			token := identifiers.New()

			must.NoError(t, relay.q.ClaimOutboxMessages(t.Context(), q, outboxdb.ClaimOutboxMessagesParams{
				ClaimedUntil:   &leaseUntil,
				ClaimedBy:      &token,
				Now:            now,
				LeaseExpiredBy: &now,
				IDs:            ids,
			}))

			rows, fetchErr := relay.q.FetchClaimedOutboxMessages(t.Context(), q, outboxdb.FetchClaimedOutboxMessagesParams{
				ClaimedBy: &token,
				IDs:       ids,
			})
			must.NoError(t, fetchErr)

			return rows
		}

		first, second = claim(asked(ids)), claim(ids)

		return nil
	}))

	return first, second
}

func TestRelay_backlog(T *testing.T) {
	T.Parallel()

	T.Run("reports depth and the age of the oldest message", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)
		w := newTestWriter(t, c)

		// Nothing pending: no depth, and an age of zero rather than a stale
		// reading left over from a previous sample.
		depth, age, err := relay.backlog(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), depth)
		test.EqOp(t, time.Duration(0), age)

		enqueue(t, client, w, Message{Topic: "orders", Payload: map[string]any{"id": "old"}})

		c.advance(90 * time.Second)

		enqueue(t, client, w, Message{Topic: "orders", Payload: map[string]any{"id": "new"}})

		depth, age, err = relay.backlog(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(2), depth)

		// Age tracks the oldest message, not the newest — that is the whole
		// point of the signal.
		test.EqOp(t, 90*time.Second, age)

		// Publishing drains it back to nothing.
		relay.cycle(t.Context())
		test.SliceLen(t, 2, rec.payloads())

		depth, age, err = relay.backlog(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), depth)
		test.EqOp(t, time.Duration(0), age)
	})

	T.Run("excludes quarantined messages", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c)

		rec.fail(platformerrors.New("poison"))
		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "a"}})

		for range 3 {
			relay.cycle(t.Context())
			c.advance(time.Hour)
		}

		must.EqOp(t, 1, countRows(t, client, "quarantined = TRUE"))

		// A permanently broken message must not read as a permanently growing
		// backlog, or the signal is useless on exactly the day it matters.
		depth, age, err := relay.backlog(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), depth)
		test.EqOp(t, time.Duration(0), age)
	})

	T.Run("sampleBacklog records without erroring", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, c)

		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "a"}})

		relay.sampleBacklog(t.Context())
	})
}

func TestRelay_reap(T *testing.T) {
	T.Parallel()

	T.Run("deletes published rows past retention and keeps the rest", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, _ := newTestRelay(t, client, c)

		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "a"}})
		relay.cycle(t.Context())

		test.EqOp(t, 1, countRows(t, client, "published_at IS NOT NULL"))

		// Inside the retention window: nothing to do.
		relay.reap(t.Context())
		test.EqOp(t, 1, countRows(t, client, "1=1"))

		c.advance(DefaultRetention + time.Hour)

		// An unpublished message must survive the reap regardless of age.
		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "b"}})

		relay.reap(t.Context())

		test.EqOp(t, 0, countRows(t, client, "published_at IS NOT NULL"))
		test.EqOp(t, 1, countRows(t, client, "published_at IS NULL"))
	})
}

func TestRelay_lifecycle(T *testing.T) {
	T.Parallel()

	T.Run("Close claims no batch on the way out", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		client := newTestClient(t)
		relay, rec := newTestRelay(t, client, c, func(cfg *RelayConfig) {
			// Long enough that no tick fires: anything published here was
			// published by a cycle Close started, and Close starts none.
			cfg.PollInterval = time.Hour
			cfg.ReapInterval = time.Hour
		})

		enqueue(t, client, newTestWriter(t, c), Message{Topic: "orders", Payload: map[string]any{"id": "a"}})

		go relay.Run()

		must.NoError(t, relay.Close(t.Context()))

		// The message is left where it was committed, unleased and unpublished,
		// for whichever relay is still running to take on its next cycle.
		test.SliceEmpty(t, rec.payloads())
		test.EqOp(t, 1, countRows(t, client, "published_at IS NULL AND claimed_by IS NULL"))

		// Close is idempotent.
		must.NoError(t, relay.Close(t.Context()))
	})
}

func TestNewRelay(T *testing.T) {
	T.Parallel()

	T.Run("rejects missing dependencies", func(t *testing.T) {
		t.Parallel()

		cfg := &RelayConfig{}

		_, err := NewRelay(t.Context(), nil, nil, nil)
		test.Error(t, err)

		_, err = NewRelay(t.Context(), cfg, nil, &messagequeuemock.PublisherProviderMock{})
		test.ErrorIs(t, err, ErrNilDatabaseClient)

		_, err = NewRelay(t.Context(), cfg, newTestClient(t), nil)
		test.ErrorIs(t, err, ErrNilPublisherProvider)
	})

	T.Run("rejects an unusable table name", func(t *testing.T) {
		t.Parallel()

		_, err := NewRelay(
			t.Context(),
			&RelayConfig{TablePrefix: "outbox; DROP TABLE users"},
			newTestClient(t),
			&messagequeuemock.PublisherProviderMock{},
		)
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
	})

	T.Run("downgrades SKIP LOCKED on a dialect that cannot do it", func(t *testing.T) {
		t.Parallel()

		// The downgrade happens once the dialect is known, which is when
		// NewRelay reads it off the client — SQLite has no SKIP LOCKED.
		cfg := &RelayConfig{ClaimMode: ClaimSkipLocked}

		r, err := NewRelay(t.Context(), cfg, newTestClient(t), &messagequeuemock.PublisherProviderMock{})
		must.NoError(t, err)

		test.EqOp(t, ClaimLease, r.cfg.ClaimMode)
	})
}
