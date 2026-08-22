package metering

import (
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runStoreSuite is the behavioral suite, run against every dialect.
//
// It is one function rather than a set of top-level tests so that SQLite and the
// container-backed servers cannot drift apart: a behavior asserted here is
// asserted everywhere, and a dialect-specific bug — MySQL's ON DUPLICATE KEY
// spelling, Postgres's numbered placeholders, the row-value IN lists, the partial
// indexes — shows up as a failure in the same named subtest rather than as a gap
// nobody noticed.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	suiteRecord(t, env)
	suiteAggregations(t, env)
	suiteConsume(t, env)
	suiteFlushLifecycle(t, env)
	suiteReap(t, env)
}

func TestSQLStore_SQLite(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// A batch of nothing is answered by the store rather than asked of the
// database. The query would be valid and would return nothing either way, so
// the only way to tell the shortcut from its absence is to take the database
// away: a deployment whose configuration resolved to zero should not be paying
// a round trip per interval to be told what its own configuration already said.
func TestSQLStore_NonPositiveLimits(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)
	store := env.newStore(T)

	must.NoError(T, env.client.Close())

	claimed, err := store.ClaimFlushable(T.Context(), baseTime, 0, 5, baseTime.Add(time.Minute))
	test.NoError(T, err)
	test.SliceEmpty(T, claimed)

	reaped, err := store.ReapEvents(T.Context(), baseTime, 0)
	test.NoError(T, err)
	test.EqOp(T, int64(0), reaped)
}

// A pass that keeps coming back full is a pass that is not keeping up, and what
// it is failing to post is revenue. Nothing else says so: the claim returns a
// batch and the flusher posts it, and both look identical whether the batch was
// everything there was or everything that fit.
func TestSQLStore_ClaimFlushable_FullBatch(T *testing.T) {
	T.Parallel()

	const filled = "metering flush filled its batch; usage may be accumulating faster than it is flushed"

	env := newSQLiteEnv(T)

	T.Run("says so when the batch comes back full", func(t *testing.T) {
		t.Parallel()

		logger := newRecordingLogger()
		store := env.newStoreWithLogger(t, logger)

		for _, subject := range []string{"a", "b"} {
			entry := newEntry("req-"+subject, 1, AggregationSum)
			entry.Subject = subject

			must.NoError(t, mustRecord(t, store, entry))
		}

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 2, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 2, claimed)

		test.SliceContains(t, logger.messages(logging.InfoLevel), filled)
	})

	T.Run("says nothing when the batch had room to spare", func(t *testing.T) {
		t.Parallel()

		logger := newRecordingLogger()
		store := env.newStoreWithLogger(t, logger)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 1, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 2, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		test.SliceNotContains(t, logger.messages(logging.InfoLevel), filled)
	})
}

// bogusDialectClient reports a dialect this package cannot emit SQL for.
//
// The unsupported-dialect branch is otherwise unreachable: the dialect comes
// from the client rather than the caller, and every client this module ships
// reports one of the three supported dialects. Only Dialect is consulted before
// the constructor gives up, so the embedded Client is never called.
type bogusDialectClient struct {
	database.Client
}

func (bogusDialectClient) Dialect() dialect.Dialect { return "oracle" }

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("refuses an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(bogusDialectClient{env.client})

		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil)

		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("refuses a prefix that is not an identifier", func(t *testing.T) {
		t.Parallel()

		// The prefix is interpolated into query text rather than bound, so it is
		// vetted rather than escaped — and vetted against every name it renders,
		// not just against itself.
		_, err := NewSQLStore(env.client, WithTablePrefix("drop; --"))

		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
	})

	T.Run("ignores nil and empty options", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(env.client, nil, WithTablePrefix(""))
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}

func suiteRecord(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("records and totals", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		result, err := store.Record(t.Context(), []Entry{
			newEntry("req-1", 3, AggregationSum),
			newEntry("req-2", 4, AggregationSum),
		}, baseTime)
		must.NoError(t, err)

		test.EqOp(t, 2, result.Accepted)
		test.EqOp(t, 0, result.Duplicates)

		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)

		test.EqOp(t, int64(7), total.Quantity)
		test.EqOp(t, monthBounds.Start, total.PeriodStart)
		test.EqOp(t, monthBounds.End, total.PeriodEnd)
		test.EqOp(t, AggregationSum, total.Aggregation)
		test.EqOp(t, int64(0), total.FlushedQuantity)
		test.EqOp(t, 0, total.FlushSequence)
	})

	t.Run("dedupes on the idempotency key", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 5, AggregationSum)))

		// The retry a client makes on a timeout, presenting the same key. It must
		// be counted once or the invoice is wrong.
		result, err := store.Record(t.Context(), []Entry{
			newEntry("req-1", 5, AggregationSum),
			newEntry("req-2", 2, AggregationSum),
		}, baseTime)
		must.NoError(t, err)

		test.EqOp(t, 1, result.Accepted)
		test.EqOp(t, 1, result.Duplicates)

		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)
		test.EqOp(t, int64(7), total.Quantity)
	})

	t.Run("dedupes within one batch", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// A redelivered batch generally overlaps rather than repeats, so the
		// duplicate must not poison the records beside it.
		result, err := store.Record(t.Context(), []Entry{
			newEntry("req-1", 5, AggregationSum),
			newEntry("req-1", 5, AggregationSum),
			newEntry("req-2", 1, AggregationSum),
		}, baseTime)
		must.NoError(t, err)

		test.EqOp(t, 2, result.Accepted)
		test.EqOp(t, 1, result.Duplicates)

		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)
		test.EqOp(t, int64(6), total.Quantity)
	})

	t.Run("keeps subjects, meters, and periods apart", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		other := newEntry("req-2", 100, AggregationSum)
		other.Subject = "account-2"

		nextMonth := newEntry("req-3", 1000, AggregationSum)
		nextMonth.Bounds = Bounds{Start: monthBounds.End, End: monthBounds.End.AddDate(0, 1, 0)}

		must.NoError(t, mustRecord(t, store,
			newEntry("req-1", 5, AggregationSum), other, nextMonth))

		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)
		test.EqOp(t, int64(5), total.Quantity)

		otherTotal, err := store.Total(t.Context(), "account-2", testMeter, monthBounds)
		must.NoError(t, err)
		test.EqOp(t, int64(100), otherTotal.Quantity)
	})

	t.Run("reports an unrecorded period as zero rather than missing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// An absent row is a number, not a missing value. Returning an error here
		// would make every read path branch on the ordinary case of a period that
		// has just begun.
		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)

		test.EqOp(t, int64(0), total.Quantity)
		test.EqOp(t, monthBounds.Start, total.PeriodStart)
		test.EqOp(t, monthBounds.End, total.PeriodEnd)
	})

	t.Run("stores dimensions without letting them split the total", func(t *testing.T) {
		t.Parallel()

		store, prefix := env.newStoreWithPrefix(t)

		first := newEntry("req-1", 3, AggregationSum)
		first.Dimensions = map[string]string{"model": "haiku", "region": "us"}

		second := newEntry("req-2", 4, AggregationSum)
		second.Dimensions = map[string]string{"model": "opus"}

		must.NoError(t, mustRecord(t, store, first, second))

		// Two dimensioned events, one total. Dimensions describe; they do not
		// enforce — a dimensioned quota's row count is the product of every
		// dimension's cardinality, and user input has no bound.
		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)
		test.EqOp(t, int64(7), total.Quantity)
		test.EqOp(t, 2, countRows(t, env, prefix+"_metering_events"))
		test.EqOp(t, 1, countRows(t, env, prefix+"_metering_totals"))
	})

	t.Run("records nothing for an empty batch", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		result, err := store.Record(t.Context(), nil, baseTime)
		must.NoError(t, err)
		test.EqOp(t, 0, result.Accepted)

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			txResult, txErr := store.RecordTx(t.Context(), q, nil, baseTime)
			test.EqOp(t, 0, txResult.Accepted)

			return txErr
		}))
	})

	t.Run("records in the caller's transaction", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// The usage and the work it describes are one fact. A crash between them
		// leaves usage counted for work that rolled back, or work committed that
		// nobody was billed for.
		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			result, err := store.RecordTx(t.Context(), q, []Entry{newEntry("req-1", 9, AggregationSum)}, baseTime)
			test.EqOp(t, 1, result.Accepted)

			return err
		}))

		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)
		test.EqOp(t, int64(9), total.Quantity)
	})

	t.Run("rolls the whole batch back with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		test.ErrorIs(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			_, err := store.RecordTx(t.Context(), q, []Entry{newEntry("req-1", 9, AggregationSum)}, baseTime)
			must.NoError(t, err)

			return errArbitrary
		}), errArbitrary)

		// Including the ledger row — so the key is free for the retry, which is
		// what makes rolling back safe rather than a permanently lost record.
		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)
		test.EqOp(t, int64(0), total.Quantity)

		result, err := store.Record(t.Context(), []Entry{newEntry("req-1", 9, AggregationSum)}, baseTime)
		must.NoError(t, err)
		test.EqOp(t, 1, result.Accepted)
	})

	t.Run("refuses RecordTx with no executor", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.RecordTx(t.Context(), nil, []Entry{newEntry("req-1", 1, AggregationSum)}, baseTime)

		test.ErrorIs(t, err, ErrNilExecutor)
	})

	t.Run("survives concurrent folds into one period", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		const writers = 8

		var wg sync.WaitGroup

		for i := range writers {
			wg.Go(func() {
				// The fold is arithmetic in the UPDATE, not a read-modify-write,
				// so concurrent recorders cannot lose one of the two.
				_, _ = store.Record(t.Context(),
					[]Entry{newEntry("req-"+string(rune('a'+i)), 1, AggregationSum)}, baseTime)
			})
		}

		wg.Wait()

		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)
		test.EqOp(t, int64(writers), total.Quantity)
	})
}

func suiteAggregations(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("sum adds across statements", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("a", 3, AggregationSum)))
		must.NoError(t, mustRecord(t, store, newEntry("b", 4, AggregationSum)))

		test.EqOp(t, int64(7), totalOf(t, store))
	})

	t.Run("max keeps the high-water mark", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// A gigabyte held all month is one gigabyte, not thirty — which is why
		// storage cannot simply be summed.
		must.NoError(t, mustRecord(t, store, newEntryAt("a", 100, AggregationMax, baseTime)))
		must.NoError(t, mustRecord(t, store, newEntryAt("b", 40, AggregationMax, baseTime.Add(time.Hour))))
		test.EqOp(t, int64(100), totalOf(t, store))

		must.NoError(t, mustRecord(t, store, newEntryAt("c", 250, AggregationMax, baseTime.Add(2*time.Hour))))
		test.EqOp(t, int64(250), totalOf(t, store))
	})

	t.Run("last takes the newest reading", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntryAt("a", 10, AggregationLast, baseTime)))
		must.NoError(t, mustRecord(t, store, newEntryAt("b", 4, AggregationLast, baseTime.Add(time.Hour))))

		test.EqOp(t, int64(4), totalOf(t, store))
	})

	t.Run("last ignores a record that arrives out of order", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntryAt("a", 10, AggregationLast, baseTime.Add(time.Hour))))

		// A queue redelivering an hour behind must not reset a gauge to an hour
		// ago, which is the whole difference between "last" and "most recently
		// ingested".
		must.NoError(t, mustRecord(t, store, newEntryAt("b", 4, AggregationLast, baseTime)))

		test.EqOp(t, int64(10), totalOf(t, store))
	})

	t.Run("folds a batch in one statement identically to one at a time", func(t *testing.T) {
		t.Parallel()

		batched := env.newStore(t)
		serial := env.newStore(t)

		entries := []Entry{
			newEntryAt("a", 10, AggregationLast, baseTime),
			newEntryAt("b", 4, AggregationLast, baseTime.Add(2*time.Hour)),
			newEntryAt("c", 7, AggregationLast, baseTime.Add(time.Hour)),
		}

		must.NoError(t, mustRecord(t, batched, entries...))

		for i := range entries {
			must.NoError(t, mustRecord(t, serial, entries[i]))
		}

		// The in-process fold and the SQL fold are the same function, so grouping
		// a batch cannot change the answer.
		test.EqOp(t, totalOf(t, serial), totalOf(t, batched))
		test.EqOp(t, int64(4), totalOf(t, batched))
	})

	t.Run("keeps last_occurred_at monotone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntryAt("a", 1, AggregationSum, baseTime.Add(time.Hour))))
		must.NoError(t, mustRecord(t, store, newEntryAt("b", 1, AggregationSum, baseTime)))

		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)

		test.EqOp(t, baseTime.Add(time.Hour), total.LastOccurredAt)
	})
}

func suiteConsume(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("allows under the limit and records", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		decision, err := store.Consume(t.Context(), newEntry("req-1", 30, AggregationSum), 100, BehaviorBlock, baseTime)
		must.NoError(t, err)

		test.True(t, decision.Allowed)
		test.EqOp(t, int64(30), decision.Used)
		test.EqOp(t, int64(100), decision.Limit)
		test.EqOp(t, int64(0), decision.Overage)
		test.EqOp(t, monthBounds.End, decision.ResetsAt)
		test.False(t, decision.Duplicate)
		test.EqOp(t, int64(30), totalOf(t, store))
	})

	t.Run("blocks past the limit and records nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("seed", 95, AggregationSum)))

		decision, err := store.Consume(t.Context(), newEntry("req-1", 10, AggregationSum), 100, BehaviorBlock, baseTime)
		must.NoError(t, err)

		test.False(t, decision.Allowed)
		// The reported total is what is actually recorded, not what the refused
		// call would have taken it to.
		test.EqOp(t, int64(95), decision.Used)
		test.EqOp(t, int64(95), totalOf(t, store))
	})

	t.Run("leaves the key free after a block", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("seed", 95, AggregationSum)))

		blocked, err := store.Consume(t.Context(), newEntry("req-1", 10, AggregationSum), 100, BehaviorBlock, baseTime)
		must.NoError(t, err)
		must.False(t, blocked.Allowed)

		// Burning the key on a consume that recorded nothing would make the
		// caller's retry — after they upgraded, say — look like a duplicate and be
		// answered with a total that never included their usage.
		allowed, err := store.Consume(t.Context(), newEntry("req-1", 10, AggregationSum), 1000, BehaviorBlock, baseTime)
		must.NoError(t, err)

		test.True(t, allowed.Allowed)
		test.False(t, allowed.Duplicate)
		test.EqOp(t, int64(105), totalOf(t, store))
	})

	t.Run("allows and records past the limit for warn and allow_overage", func(t *testing.T) {
		t.Parallel()

		for _, behavior := range []QuotaBehavior{BehaviorWarn, BehaviorAllowOverage} {
			store := env.newStore(t)

			must.NoError(t, mustRecord(t, store, newEntry("seed", 95, AggregationSum)))

			decision, err := store.Consume(t.Context(),
				newEntry("req-1", 10, AggregationSum), 100, behavior, baseTime)
			must.NoError(t, err)

			test.True(t, decision.Allowed, test.Sprintf("behavior %q", behavior))
			test.EqOp(t, int64(105), decision.Used, test.Sprintf("behavior %q", behavior))
			test.EqOp(t, int64(5), decision.Overage, test.Sprintf("behavior %q", behavior))
			test.EqOp(t, int64(105), totalOf(t, store), test.Sprintf("behavior %q", behavior))
		}
	})

	t.Run("reports a duplicate against the true total", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first, err := store.Consume(t.Context(), newEntry("req-1", 30, AggregationSum), 100, BehaviorBlock, baseTime)
		must.NoError(t, err)
		must.False(t, first.Duplicate)

		second, err := store.Consume(t.Context(), newEntry("req-1", 30, AggregationSum), 100, BehaviorBlock, baseTime)
		must.NoError(t, err)

		test.True(t, second.Duplicate)
		test.True(t, second.Allowed)
		// The retried request should see its own usage already in there, not a
		// projection that counts it twice.
		test.EqOp(t, int64(30), second.Used)
		test.EqOp(t, int64(30), totalOf(t, store))
	})

	t.Run("serializes concurrent consumes against one limit", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		const (
			consumers = 8
			limit     = 5
		)

		var (
			mu      sync.Mutex
			allowed int
		)

		var wg sync.WaitGroup

		for i := range consumers {
			wg.Go(func() {
				decision, err := store.Consume(t.Context(),
					newEntry("req-"+string(rune('a'+i)), 1, AggregationSum), limit, BehaviorBlock, baseTime)
				if err != nil || decision == nil || !decision.Allowed {
					return
				}

				mu.Lock()
				defer mu.Unlock()

				allowed++
			})
		}

		wg.Wait()

		// Exactly the limit gets through. Without the lock, two consumers both
		// see room and both take the last unit.
		test.EqOp(t, limit, allowed)
		test.EqOp(t, int64(limit), totalOf(t, store))
	})

	t.Run("opens a period it is the first to touch", func(t *testing.T) {
		t.Parallel()

		store, prefix := env.newStoreWithPrefix(t)

		// A subject's first consume in a period has no row to lock, and two
		// concurrent first consumes would both find nothing and both take the last
		// unit. The zero row is what they serialize on.
		_, err := store.Consume(t.Context(), newEntry("req-1", 1, AggregationSum), 100, BehaviorBlock, baseTime)
		must.NoError(t, err)

		test.EqOp(t, 1, countRows(t, env, prefix+"_metering_totals"))
	})

	t.Run("consumes against a max meter's high-water mark", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntryAt("seed", 90, AggregationMax, baseTime)))

		// 40 is under the limit on its own and does not raise the mark, so it is
		// allowed and the total stands. A sum meter would have refused it.
		decision, err := store.Consume(t.Context(),
			newEntryAt("req-1", 40, AggregationMax, baseTime.Add(time.Hour)), 100, BehaviorBlock, baseTime)
		must.NoError(t, err)

		test.True(t, decision.Allowed)
		test.EqOp(t, int64(90), decision.Used)
		test.EqOp(t, int64(90), totalOf(t, store))
	})
}

func suiteFlushLifecycle(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("claims what owes the provider", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		test.EqOp(t, testSubject, claimed[0].Subject)
		test.EqOp(t, testMeter, claimed[0].Meter)
		test.EqOp(t, int64(42), claimed[0].Quantity)
		test.EqOp(t, int64(42), claimed[0].Delta())
		// Incremented at claim rather than at failure, so a total whose provider
		// call reliably kills the process eventually gives up.
		test.EqOp(t, 1, claimed[0].FlushAttempts)
	})

	t.Run("does not claim a settled total", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		must.NoError(t, store.MarkFlushed(t.Context(), claimed[0], 42, baseTime))

		again, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		test.SliceEmpty(t, again)
	})

	t.Run("does not claim a leased total", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		first, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 1, first)

		second, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		test.SliceEmpty(t, second)

		// Once the lease lapses, a second flusher may take over — which is what
		// makes a crashed flusher recoverable rather than a permanent stall.
		third, err := store.ClaimFlushable(t.Context(), baseTime.Add(2*time.Minute), 10, 5, baseTime.Add(3*time.Minute))
		must.NoError(t, err)
		test.SliceLen(t, 1, third)
	})

	t.Run("does not claim a total that has spent its attempts", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 1, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		must.NoError(t, store.ReleaseFlush(t.Context(), claimed[0], "boom", baseTime))

		// Not retried forever: a customer that was deleted would otherwise cost a
		// provider call every interval and bury the totals that would succeed.
		again, err := store.ClaimFlushable(t.Context(), baseTime, 10, 1, baseTime.Add(time.Minute))
		must.NoError(t, err)
		test.SliceEmpty(t, again)
	})

	t.Run("honors the batch limit and returns nothing for a non-positive one", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		for _, subject := range []string{"a", "b", "c"} {
			entry := newEntry("req-"+subject, 1, AggregationSum)
			entry.Subject = subject

			must.NoError(t, mustRecord(t, store, entry))
		}

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 2, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		test.SliceLen(t, 2, claimed)

		none, err := store.ClaimFlushable(t.Context(), baseTime, 0, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		test.SliceEmpty(t, none)
	})

	t.Run("advances the sequence on a settle", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.NoError(t, store.MarkFlushed(t.Context(), claimed[0], 42, baseTime))

		must.NoError(t, mustRecord(t, store, newEntry("req-2", 8, AggregationSum)))

		next, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 1, next)

		test.EqOp(t, 1, next[0].FlushSequence)
		test.EqOp(t, int64(42), next[0].FlushedQuantity)
		// The delta, not the total. Posting the running total every flush would
		// invoice the sum of every partial total ever posted.
		test.EqOp(t, int64(8), next[0].Delta())
		// The attempt count resets on success, so an intermittent provider does
		// not eventually exhaust a healthy total's budget.
		test.EqOp(t, 1, next[0].FlushAttempts)
	})

	t.Run("refuses a settle at a stale sequence", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.NoError(t, store.MarkFlushed(t.Context(), claimed[0], 42, baseTime))

		// The flusher whose lease lapsed mid-post, coming back to settle. Letting
		// it advance a sequence somebody else has moved is how the same delta ends
		// up on the wire under two different keys — the one race an idempotency
		// key cannot undo.
		test.Error(t, store.MarkFlushed(t.Context(), claimed[0], 42, baseTime))
	})

	t.Run("refuses a release at a stale sequence", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.NoError(t, store.MarkFlushed(t.Context(), claimed[0], 42, baseTime))

		test.Error(t, store.ReleaseFlush(t.Context(), claimed[0], "boom", baseTime))
	})

	t.Run("release schedules the retry and keeps the flushed quantity", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)

		must.NoError(t, store.ReleaseFlush(t.Context(), claimed[0], "provider timed out", baseTime.Add(time.Hour)))

		// Not claimable until the scheduled retry.
		early, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		test.SliceEmpty(t, early)

		later, err := store.ClaimFlushable(t.Context(), baseTime.Add(2*time.Hour), 10, 5, baseTime.Add(3*time.Hour))
		must.NoError(t, err)
		must.SliceLen(t, 1, later)

		test.EqOp(t, "provider timed out", later[0].LastError)
		// The sequence did not move, so the retry posts the same delta under the
		// same key and the provider deduplicates it.
		test.EqOp(t, 0, later[0].FlushSequence)
		test.EqOp(t, int64(0), later[0].FlushedQuantity)
		test.EqOp(t, 2, later[0].FlushAttempts)
	})

	t.Run("refuses a nil total", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		test.Error(t, store.MarkFlushed(t.Context(), nil, 1, baseTime))
		test.Error(t, store.ReleaseFlush(t.Context(), nil, "boom", baseTime))
	})
}

func suiteReap(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("reaps events whose period has settled", func(t *testing.T) {
		t.Parallel()

		store, prefix := env.newStoreWithPrefix(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.NoError(t, store.MarkFlushed(t.Context(), claimed[0], 42, baseTime))

		reaped, err := store.ReapEvents(t.Context(), baseTime.Add(time.Hour), 100)
		must.NoError(t, err)

		test.EqOp(t, int64(1), reaped)
		test.EqOp(t, 0, countRows(t, env, prefix+"_metering_events"))
		// The total survives; only its evidence is retired.
		test.EqOp(t, 1, countRows(t, env, prefix+"_metering_totals"))
	})

	t.Run("spares events whose period still owes the provider", func(t *testing.T) {
		t.Parallel()

		store, prefix := env.newStoreWithPrefix(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		// Deleting it would take the evidence for an unposted invoice line, and —
		// worse — re-open the idempotency key, so a redelivery would be counted a
		// second time into a total nobody has invoiced yet.
		reaped, err := store.ReapEvents(t.Context(), baseTime.Add(time.Hour), 100)
		must.NoError(t, err)

		test.EqOp(t, int64(0), reaped)
		test.EqOp(t, 1, countRows(t, env, prefix+"_metering_events"))
	})

	t.Run("spares events inside the retention window", func(t *testing.T) {
		t.Parallel()

		store, prefix := env.newStoreWithPrefix(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.NoError(t, store.MarkFlushed(t.Context(), claimed[0], 42, baseTime))

		reaped, err := store.ReapEvents(t.Context(), baseTime.Add(-time.Hour), 100)
		must.NoError(t, err)

		test.EqOp(t, int64(0), reaped)
		test.EqOp(t, 1, countRows(t, env, prefix+"_metering_events"))
	})

	t.Run("honors the batch limit and does nothing for a non-positive one", func(t *testing.T) {
		t.Parallel()

		store, prefix := env.newStoreWithPrefix(t)

		must.NoError(t, mustRecord(t, store,
			newEntry("req-1", 1, AggregationSum),
			newEntry("req-2", 1, AggregationSum),
			newEntry("req-3", 1, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.NoError(t, store.MarkFlushed(t.Context(), claimed[0], 3, baseTime))

		none, err := store.ReapEvents(t.Context(), baseTime.Add(time.Hour), 0)
		must.NoError(t, err)
		test.EqOp(t, int64(0), none)

		reaped, err := store.ReapEvents(t.Context(), baseTime.Add(time.Hour), 2)
		must.NoError(t, err)

		// Capped, so a long-neglected table is trimmed over several passes rather
		// than one statement that holds locks for minutes.
		test.EqOp(t, int64(2), reaped)
		test.EqOp(t, 1, countRows(t, env, prefix+"_metering_events"))
	})
}

// mustRecord records entries and asserts the store accepted the call.
func mustRecord(t *testing.T, store Store, entries ...Entry) error {
	t.Helper()

	_, err := store.Record(t.Context(), entries, baseTime)

	return err
}

// totalOf reads the suite's usual subject and meter for the month.
func totalOf(t *testing.T, store Store) int64 {
	t.Helper()

	total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
	must.NoError(t, err)

	return total.Quantity
}
