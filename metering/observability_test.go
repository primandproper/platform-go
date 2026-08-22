package metering

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file holds one rule, asserted in one place: every span about a single
// subject, meter, and period carries the whole window, and carries the meter's
// aggregation wherever the emitting path already has it.
//
// The window matters in both halves. A total's period is recoverable from its
// start alone only for the calendar periods; a billing period an application
// resolved per subject, or a boundary that moved, is exactly the case somebody
// reads a trace to understand. The aggregation matters because a total is a
// number whose meaning depends on it — a sum, a peak, and a last-value reading
// look identical on a span and reconcile to three different invoices.
//
// Batch-level operations are deliberately absent. DurableRecorder.record and the
// store's Record, RecordTx, ClaimFlushable, and ReapEvents each span many
// meters and many periods, and a single window on those spans would be a claim
// none of them can make.

// observedTime reads a time an operation observed.
//
// Callers compare the result with Equal rather than by equality, because a bound
// that has been through the database and back is the same instant as the literal
// it was written from without being the same struct.
func observedTime(t *testing.T, op *observability.RecordingOperation, key string) time.Time {
	t.Helper()

	value, ok := op.Values[key]
	must.True(t, ok, must.Sprintf("operation did not observe %q", key))

	at, ok := value.(time.Time)
	must.True(t, ok, must.Sprintf("%q was observed as %T, not a time.Time", key, value))

	return at
}

// observedAggregation reads the aggregation an operation observed, which is
// recorded rendered rather than as an Aggregation so a trace backend shows the
// name and not a Go type.
func observedAggregation(t *testing.T, op *observability.RecordingOperation) string {
	t.Helper()

	value, ok := op.Values[aggregationKey]
	must.True(t, ok, must.Sprintf("operation did not observe %q", aggregationKey))

	aggregation, ok := value.(string)
	must.True(t, ok, must.Sprintf("%q was observed as %T, not a string", aggregationKey, value))

	return aggregation
}

// observedWindow asserts that some operation the observer recorded carried both
// bounds, and returns it so a caller can go on to assert the aggregation.
func observedWindow(t *testing.T, obs *observability.RecordingObserver, bounds Bounds) *observability.RecordingOperation {
	t.Helper()

	op := obs.ObservedOperationWithKeys(t, periodStartKey, periodEndKey)

	test.True(t, observedTime(t, op, periodStartKey).Equal(bounds.Start))
	test.True(t, observedTime(t, op, periodEndKey).Equal(bounds.End))

	return op
}

func TestQuotaEnforcer_observesThePeriodAndAggregation(T *testing.T) {
	T.Parallel()

	T.Run("on a check", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		obs := observability.NewRecordingObserver()
		env.enforcer.o11y = obs

		_, err := env.enforcer.Check(t.Context(), testSubject, testMeter, 1)
		must.NoError(t, err)

		op := observedWindow(t, obs, monthBounds)
		test.EqOp(t, string(AggregationSum), observedAggregation(t, op))
	})

	T.Run("on a consume", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		obs := observability.NewRecordingObserver()
		env.enforcer.o11y = obs

		_, err := env.enforcer.ConsumeUsage(t.Context(), Usage{
			Subject:        testSubject,
			Meter:          testMeter,
			Quantity:       1,
			IdempotencyKey: "req-1",
			OccurredAt:     baseTime,
		})
		must.NoError(t, err)

		op := observedWindow(t, obs, monthBounds)
		test.EqOp(t, string(AggregationSum), observedAggregation(t, op))
	})

	T.Run("on a check whose durable read failed open", func(t *testing.T) {
		t.Parallel()

		// The annotation is attached at the resolve, before anything can go
		// wrong with the read, so the one decision that was derived from no
		// reading at all still says which window it was about.
		env := newTestEnforcer(t, BehaviorBlock, 100,
			WithEnforcerCache(nil))
		env.enforcer.store = &failingTotalStore{Store: env.store}
		env.enforcer.totals = nil
		env.enforcer.cfg.FailOpen = true

		obs := observability.NewRecordingObserver()
		env.enforcer.o11y = obs

		decision, err := env.enforcer.Check(t.Context(), testSubject, testMeter, 1)
		must.NoError(t, err)
		test.True(t, decision.Stale)

		op := observedWindow(t, obs, monthBounds)
		test.EqOp(t, string(AggregationSum), observedAggregation(t, op))
	})
}

func TestSQLStore_observesThePeriodAndAggregation(T *testing.T) {
	T.Parallel()

	T.Run("on a total read", func(t *testing.T) {
		t.Parallel()

		store, obs := newRecordingStore(t)

		_, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)

		// No aggregation: Total's signature does not carry one, and the store
		// never consults the Registry to find out.
		op := observedWindow(t, obs, monthBounds)
		_, ok := op.Values[aggregationKey]
		test.False(t, ok)
	})

	T.Run("on a consume", func(t *testing.T) {
		t.Parallel()

		store, obs := newRecordingStore(t)

		_, err := store.Consume(t.Context(), newEntry("req-1", 1, AggregationSum),
			100, BehaviorBlock, baseTime)
		must.NoError(t, err)

		op := observedWindow(t, obs, monthBounds)
		test.EqOp(t, string(AggregationSum), observedAggregation(t, op))
	})

	T.Run("on the guarded settles", func(t *testing.T) {
		t.Parallel()

		// MarkFlushed and ReleaseFlush are guarded UPDATEs against one totals
		// row, which is keyed by the period. When execExpectingRow reports that
		// the guard matched nothing, the window is what identifies the row that
		// got away.
		store := newSQLiteEnv(t).newStore(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)

		// A fresh observer per settle, so the operation each assertion matches
		// is the one the call under test began and not the read that set it up.
		// Release before mark: MarkFlushed advances the sequence both are
		// guarded on, which would leave the in-hand total stale.
		obs := recordObservations(t, store)
		must.NoError(t, store.ReleaseFlush(t.Context(), total, "", baseTime))

		op := observedWindow(t, obs, monthBounds)
		test.EqOp(t, string(AggregationSum), observedAggregation(t, op))

		obs = recordObservations(t, store)
		must.NoError(t, store.MarkFlushed(t.Context(), total, total.Quantity, baseTime))

		op = observedWindow(t, obs, monthBounds)
		test.EqOp(t, string(AggregationSum), observedAggregation(t, op))
	})
}

func TestFlusher_observesThePeriodAndAggregation(T *testing.T) {
	T.Parallel()

	env := newTestFlusher(T, staticMapper("cus_123"))

	must.NoError(T, mustRecord(T, env.store, newEntry("req-1", 42, AggregationSum)))

	obs := observability.NewRecordingObserver()
	env.flusher.o11y = obs

	result, err := env.flusher.Flush(T.Context())
	must.NoError(T, err)
	test.EqOp(T, 1, result.Flushed)

	// The flusher reads its window and its aggregation off a row rather than
	// off a resolver, so this also says the two survive a round trip.
	op := observedWindow(T, obs, monthBounds)
	test.EqOp(T, string(AggregationSum), observedAggregation(T, op))
}

// newRecordingStore is a SQLite-backed store whose observations a test can read.
func newRecordingStore(t *testing.T) (Store, *observability.RecordingObserver) {
	t.Helper()

	store := newSQLiteEnv(t).newStore(t)

	return store, recordObservations(t, store)
}

// recordObservations points a store at a fresh recording observer and hands it
// back, so a test can start observing partway through rather than only at
// construction.
func recordObservations(t *testing.T, store Store) *observability.RecordingObserver {
	t.Helper()

	obs := observability.NewRecordingObserver()

	impl, ok := store.(*SQLStore)
	must.True(t, ok, must.Sprintf("store is a %T, not a *sqlStore", store))
	impl.o11y = obs

	return obs
}
