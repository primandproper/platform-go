package metering

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/capitalism"
	capitalismnoop "github.com/primandproper/platform-go/v13/capitalism/noop"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/jobs"
	"github.com/primandproper/platform-go/v13/observability/logging"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// flusherEnv is one flusher with the pieces a test needs to reach around it.
type flusherEnv struct {
	flusher  *Flusher
	store    Store
	reporter *recordingReporter
	clock    *stubClock
}

// newTestFlusher builds a flusher over a fresh store and a recording reporter.
func newTestFlusher(t *testing.T, mapper ProviderMapper, opts ...FlusherOption) *flusherEnv {
	t.Helper()

	env := newSQLiteEnv(t)
	store := env.newStore(t)

	return newTestFlusherOver(t, store, mapper, opts...)
}

// newTestFlusherOver is newTestFlusher over a store the caller supplies, for the
// tests that need a partially broken one.
func newTestFlusherOver(t *testing.T, store Store, mapper ProviderMapper, opts ...FlusherOption) *flusherEnv {
	t.Helper()

	c := newStubClock()
	reporter := &recordingReporter{}

	flusher, err := NewFlusher(t.Context(), &FlusherConfig{}, store, mapper, reporter,
		append([]FlusherOption{WithFlusherClock(c)}, opts...)...)
	must.NoError(t, err)

	return &flusherEnv{flusher: flusher, store: store, reporter: reporter, clock: c}
}

func TestNewFlusher(T *testing.T) {
	T.Parallel()

	store := newSQLiteEnv(T).newStore(T)
	mapper := staticMapper("cus_123")

	T.Run("refuses a nil config, store, mapper, or reporter", func(t *testing.T) {
		t.Parallel()

		_, err := NewFlusher(t.Context(), nil, store, mapper, &recordingReporter{})
		test.Error(t, err)

		_, err = NewFlusher(t.Context(), &FlusherConfig{}, nil, mapper, &recordingReporter{})
		test.ErrorIs(t, err, ErrNilStore)

		_, err = NewFlusher(t.Context(), &FlusherConfig{}, store, nil, &recordingReporter{})
		test.ErrorIs(t, err, ErrNilProviderMapper)

		// No implicit noop: a flusher that silently posted nowhere would mark
		// usage flushed and advance the sequence, so a month of revenue would be
		// discarded by an omission in the wiring.
		_, err = NewFlusher(t.Context(), &FlusherConfig{}, store, mapper, nil)
		test.ErrorIs(t, err, ErrNilUsageReporter)
	})

	T.Run("accepts the explicit noop reporter", func(t *testing.T) {
		t.Parallel()

		// "Meter everything, bill nothing" is a supported deployment; it just has
		// to be said out loud at the call site.
		flusher, err := NewFlusher(t.Context(), &FlusherConfig{}, store, mapper,
			capitalismnoop.NewUsageReporter())
		must.NoError(t, err)
		must.NotNil(t, flusher)
	})

	T.Run("fills defaults and ignores nil options", func(t *testing.T) {
		t.Parallel()

		cfg := &FlusherConfig{}

		flusher, err := NewFlusher(t.Context(), cfg, store, mapper, &recordingReporter{}, nil,
			WithFlusherClock(nil), WithFlusherLogger(nil),
			WithFlusherTracerProvider(nil), WithFlusherMetricsProvider(nil))
		must.NoError(t, err)

		test.EqOp(t, DefaultFlushBatchSize, flusher.cfg.BatchSize)
		test.EqOp(t, DefaultMaxFlushAttempts, flusher.cfg.MaxAttempts)
		test.NotNil(t, flusher.clock)
	})

	T.Run("refuses a lease that cannot cover a post", func(t *testing.T) {
		t.Parallel()

		// A lease that expires while the post it covers is in flight is not a
		// lease, and two flushers posting the same total concurrently is the
		// duplicate charge no idempotency key can undo.
		_, err := NewFlusher(t.Context(), &FlusherConfig{
			FlushTimeout:  time.Minute,
			LeaseDuration: time.Second,
		}, store, mapper, &recordingReporter{})

		test.Error(t, err)
	})
}

func TestFlusher_Flush(T *testing.T) {
	T.Parallel()

	T.Run("posts the accumulated usage", func(t *testing.T) {
		t.Parallel()

		env := newTestFlusher(t, staticMapper("cus_123"))

		must.NoError(t, mustRecord(t, env.store, newEntry("req-1", 42, AggregationSum)))

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 1, result.Claimed)
		test.EqOp(t, 1, result.Flushed)
		test.EqOp(t, int64(42), result.Quantity)

		posts := env.reporter.recorded()
		must.SliceLen(t, 1, posts)

		test.EqOp(t, "cus_123", posts[0].CustomerID)
		test.EqOp(t, testMeter, posts[0].MeterName)
		test.EqOp(t, int64(42), posts[0].Quantity)
		test.StrHasPrefix(t, idempotencyKeyPrefix, posts[0].IdempotencyKey)
		test.EqOp(t, testMeter, posts[0].Metadata["metering_meter"])
		test.EqOp(t, testSubject, posts[0].Metadata["metering_subject"])
		test.EqOp(t, "0", posts[0].Metadata["metering_sequence"])
	})

	T.Run("posts the delta, not the running total", func(t *testing.T) {
		t.Parallel()

		env := newTestFlusher(t, staticMapper("cus_123"))

		must.NoError(t, mustRecord(t, env.store, newEntry("req-1", 42, AggregationSum)))
		_, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		must.NoError(t, mustRecord(t, env.store, newEntry("req-2", 8, AggregationSum)))
		_, err = env.flusher.Flush(t.Context())
		must.NoError(t, err)

		posts := env.reporter.recorded()
		must.SliceLen(t, 2, posts)

		// Providers aggregate the records inside a billing period. Posting the
		// running total every flush would invoice the sum of every partial total
		// ever posted.
		test.EqOp(t, int64(42), posts[0].Quantity)
		test.EqOp(t, int64(8), posts[1].Quantity)
		// A fresh sequence, so the provider accepts it rather than deduplicating
		// it against the first.
		test.NotEqOp(t, posts[0].IdempotencyKey, posts[1].IdempotencyKey)
		test.EqOp(t, "1", posts[1].Metadata["metering_sequence"])
	})

	// A pass that collected no errors returns none. The check that reports them
	// is a shortcut rather than a branch with behavior of its own — joining an
	// empty set of errors is nil, and reporting a nil error is nil — so no
	// assertion here can distinguish it from a pass that always reported. A
	// mutation report naming that line is naming an equivalent mutant.
	T.Run("posts nothing when there is nothing to post", func(t *testing.T) {
		t.Parallel()

		env := newTestFlusher(t, staticMapper("cus_123"))

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 0, result.Claimed)
		test.SliceEmpty(t, env.reporter.recorded())
	})

	T.Run("posts nothing twice for the same usage", func(t *testing.T) {
		t.Parallel()

		env := newTestFlusher(t, staticMapper("cus_123"))

		must.NoError(t, mustRecord(t, env.store, newEntry("req-1", 42, AggregationSum)))

		_, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		second, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 0, second.Claimed)
		test.SliceLen(t, 1, env.reporter.recorded())
	})

	T.Run("settles without posting when the subject does not bill for the meter", func(t *testing.T) {
		t.Parallel()

		// A free plan on a metered endpoint. Not a failure — and settled rather
		// than retried, so it does not become the permanent head of the queue.
		env := newTestFlusher(t, ProviderMapperFunc(
			func(context.Context, string, string) (ProviderRef, error) {
				return ProviderRef{}, platformerrors.Wrap(ErrNoProviderRef, "free plan")
			}))

		must.NoError(t, mustRecord(t, env.store, newEntry("req-1", 42, AggregationSum)))

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 1, result.Skipped)
		test.EqOp(t, 0, result.Flushed)
		test.SliceEmpty(t, env.reporter.recorded())

		// Settled, so it is not claimed again.
		again, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)
		test.EqOp(t, 0, again.Claimed)
	})

	T.Run("settles without posting for an empty provider ref", func(t *testing.T) {
		t.Parallel()

		env := newTestFlusher(t, zeroMapper())

		must.NoError(t, mustRecord(t, env.store, newEntry("req-1", 42, AggregationSum)))

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 1, result.Skipped)
		test.SliceEmpty(t, env.reporter.recorded())
	})

	T.Run("posts a half-filled provider ref rather than skipping it", func(t *testing.T) {
		t.Parallel()

		// A customer with no meter is a mapper bug, not a free plan. Letting it
		// reach the reporter's own validation makes it a visible failure instead of
		// usage that quietly stops being billed.
		env := newTestFlusher(t, ProviderMapperFunc(func(context.Context, string, string) (ProviderRef, error) {
			return ProviderRef{CustomerID: "cus_123"}, nil
		}))

		must.NoError(t, mustRecord(t, env.store, newEntry("req-1", 42, AggregationSum)))

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 0, result.Skipped)

		posts := env.reporter.recorded()
		must.SliceLen(t, 1, posts)
		test.EqOp(t, "cus_123", posts[0].CustomerID)
		test.EqOp(t, "", posts[0].MeterName)
	})

	T.Run("retries a total whose mapping failed", func(t *testing.T) {
		t.Parallel()

		env := newTestFlusher(t, ProviderMapperFunc(
			func(context.Context, string, string) (ProviderRef, error) {
				return ProviderRef{}, errArbitrary
			}))

		must.NoError(t, mustRecord(t, env.store, newEntry("req-1", 42, AggregationSum)))

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 1, result.Failed)
		test.EqOp(t, 0, result.Flushed)

		// A mapping failure is transient — a lookup service being down — so it
		// comes back, unlike ErrNoProviderRef.
		env.clock.advance(time.Hour)

		later, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)
		test.EqOp(t, 1, later.Claimed)
	})

	T.Run("retries a post the provider refused, under the same key", func(t *testing.T) {
		t.Parallel()

		env := newTestFlusher(t, staticMapper("cus_123"))
		env.reporter.err = errArbitrary

		must.NoError(t, mustRecord(t, env.store, newEntry("req-1", 42, AggregationSum)))

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)
		test.EqOp(t, 1, result.Failed)

		env.reporter.err = nil
		env.clock.advance(time.Hour)

		retried, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)
		test.EqOp(t, 1, retried.Flushed)

		posts := env.reporter.recorded()
		must.SliceLen(t, 1, posts)

		// The sequence did not move on the failure, so the retry carries the same
		// delta under the same key — and the provider deduplicates it if the
		// first attempt actually landed and failed on the way back.
		test.EqOp(t, "0", posts[0].Metadata["metering_sequence"])
	})

	T.Run("contains a panic from the provider SDK", func(t *testing.T) {
		t.Parallel()

		env := newTestFlusher(t, staticMapper("cus_123"))
		env.reporter.panicNow = true

		must.NoError(t, mustRecord(t, env.store, newEntry("req-1", 42, AggregationSum)))

		// A provider SDK is third-party code on the money path. A panic there
		// would otherwise take the goroutine and every other total in the batch
		// with it.
		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 1, result.Failed)
	})

	T.Run("gives up after exhausting attempts, without discarding the usage", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		env := newTestFlusherOver(t, store, staticMapper("cus_123"))
		env.flusher.cfg.MaxAttempts = 2
		env.reporter.err = errArbitrary

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		for range 3 {
			_, err := env.flusher.Flush(t.Context())
			must.NoError(t, err)

			env.clock.advance(time.Hour)
		}

		// Left where it is rather than marked flushed. Marking it would discard
		// usage nobody has been billed for; leaving it keeps the row visible and
		// the money recoverable by hand.
		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)

		test.True(t, total.Pending())
		test.EqOp(t, int64(0), total.FlushedQuantity)

		// And it stops costing a provider call every interval.
		exhausted, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)
		test.EqOp(t, 0, exhausted.Claimed)
	})

	// The backlog is the one number in a flush pass that says whether the
	// arrangement is working. Every other instrument counts work that happened;
	// this one counts work that was claimed and did not, which is what a queue
	// building up behind a steady flush rate looks like from the outside.
	T.Run("records what a pass claimed and did not settle", func(t *testing.T) {
		t.Parallel()

		const otherSubject = "account-2"

		instruments := newRecordingInstruments()
		env := newTestFlusher(t, ProviderMapperFunc(
			func(_ context.Context, subject, meter string) (ProviderRef, error) {
				if subject == otherSubject {
					return ProviderRef{}, platformerrors.Wrap(ErrNoProviderRef, "free plan")
				}

				return ProviderRef{CustomerID: "cus_123", MeterName: meter}, nil
			}), WithFlusherMetricsProvider(instruments.provider()))

		billed := newEntry("req-1", 42, AggregationSum)
		free := newEntry("req-2", 7, AggregationSum)
		free.Subject = otherSubject

		must.NoError(t, mustRecord(t, env.store, billed, free))

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		// Two claimed, one posted, one settled as unbillable: nothing is left
		// owing, and the gauge has to read zero rather than two, or one, or the
		// count of anything else this pass did.
		test.EqOp(t, 2, result.Claimed)
		test.EqOp(t, 1, result.Flushed)
		test.EqOp(t, 1, result.Skipped)
		test.Eq(t, []int64{0}, instruments.recorded("_flush_backlog"))
	})

	T.Run("records a backlog for what it could not post", func(t *testing.T) {
		t.Parallel()

		instruments := newRecordingInstruments()
		env := newTestFlusher(t, staticMapper("cus_123"),
			WithFlusherMetricsProvider(instruments.provider()))
		env.reporter.err = errArbitrary

		must.NoError(t, mustRecord(t, env.store, newEntry("req-1", 42, AggregationSum)))

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 1, result.Failed)
		test.Eq(t, []int64{1}, instruments.recorded("_flush_backlog"))
	})

	T.Run("reports a claim failure", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		env := newTestFlusherOver(t, &failingClaimStore{Store: store}, staticMapper("cus_123"))

		_, err := env.flusher.Flush(t.Context())

		test.ErrorIs(t, err, errArbitrary)
	})

	T.Run("reaps even when the posts failed", func(t *testing.T) {
		t.Parallel()

		store, prefix := newSQLiteEnv(t).newStoreWithPrefix(t)
		env := newTestFlusherOver(t, store, staticMapper("cus_123"))

		must.NoError(t, mustRecord(t, store, newEntry("settled", 10, AggregationSum)))
		_, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		// Now break the provider and add more usage. The reap is an unrelated
		// chore sharing a schedule, and a provider being unreachable is not a
		// reason to let the event table grow unbounded.
		env.reporter.err = errArbitrary
		must.NoError(t, mustRecord(t, store, newEntry("unsettled", 10, AggregationSum)))

		env.clock.advance(DefaultEventRetention + time.Hour)

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 1, result.Failed)
		// Neither event is reaped: the period now owes the provider again, and
		// the reap's own predicate refuses to touch anything a failed post still
		// needs.
		test.EqOp(t, int64(0), result.EventsReaped)
		test.EqOp(t, 2, countRows(t, newSQLiteEnvFor(t, store), prefix+"_metering_events"))
	})

	T.Run("reaps settled events", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, prefix := env.newStoreWithPrefix(t)
		instruments := newRecordingInstruments()
		flushEnv := newTestFlusherOver(t, store, staticMapper("cus_123"),
			WithFlusherMetricsProvider(instruments.provider()))

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		_, err := flushEnv.flusher.Flush(t.Context())
		must.NoError(t, err)

		// The pass before anything was old enough to reap reports no reaping at
		// all, rather than reporting that it reaped nothing. A counter fed a
		// zero on every interval is a series that never goes quiet, and a reap
		// that has stopped working looks exactly like one with nothing to do.
		test.SliceEmpty(t, instruments.recorded("_events_reaped"))

		flushEnv.clock.advance(DefaultEventRetention + time.Hour)

		result, err := flushEnv.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(1), result.EventsReaped)
		test.EqOp(t, 0, countRows(t, env, prefix+"_metering_events"))
		test.Eq(t, []int64{1}, instruments.recorded("_events_reaped"))
	})

	T.Run("skips the reap when disabled", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, prefix := env.newStoreWithPrefix(t)
		flushEnv := newTestFlusherOver(t, store, staticMapper("cus_123"))
		flushEnv.flusher.cfg.DisableReap = true

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		_, err := flushEnv.flusher.Flush(t.Context())
		must.NoError(t, err)

		flushEnv.clock.advance(DefaultEventRetention + time.Hour)

		result, err := flushEnv.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(0), result.EventsReaped)
		test.EqOp(t, 1, countRows(t, env, prefix+"_metering_events"))
	})

	T.Run("reports a reap failure without losing the flush result", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		env := newTestFlusherOver(t, &failingReapStore{Store: store}, staticMapper("cus_123"))

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		result, err := env.flusher.Flush(t.Context())

		test.ErrorIs(t, err, errArbitrary)
		// The posts still happened and are still reported, because the two are
		// unrelated chores.
		must.NotNil(t, result)
		test.EqOp(t, 1, result.Flushed)
	})

	T.Run("survives a settle that fails after the provider has the usage", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		env := newTestFlusherOver(t, &failingSettleStore{Store: store}, staticMapper("cus_123"))

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		// The provider has it and the row does not say so. The next pass posts
		// the same delta under the same sequence and the provider deduplicates it
		// — which is the whole reason the key is derived from the sequence.
		test.EqOp(t, 1, result.Failed)
		test.SliceLen(t, 1, env.reporter.recorded())
	})

	T.Run("survives a release that fails", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		logger := newRecordingLogger()
		env := newTestFlusherOver(t, &failingReleaseStore{Store: store}, staticMapper("cus_123"),
			WithFlusherLogger(logger))
		env.reporter.err = errArbitrary

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		// The lease simply expires instead. Slower than an explicit release, and
		// the total is picked up again either way.
		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 1, result.Failed)

		// Survived is not swallowed. A release that keeps failing is a lease
		// held to its full duration on every retry, which is a flush loop
		// getting slower for a reason nothing else reports.
		test.SliceContains(t, logger.messages(logging.ErrorLevel), "releasing metering flush lease")
	})

	// Where the ceiling sits is the only interesting property of an attempt
	// budget, and it decides whether a total is retried forever or written off:
	// the row is left unbilled either way, but the abandonment is the notice
	// that somebody has to go and collect it by hand.
	T.Run("abandons a total on the attempt that reaches the ceiling", func(t *testing.T) {
		t.Parallel()

		const abandoned = "abandoning metering flush after exhausting attempts; usage is recorded but unbilled"

		store := newSQLiteEnv(t).newStore(t)
		logger := newRecordingLogger()
		instruments := newRecordingInstruments()
		env := newTestFlusherOver(t, store, staticMapper("cus_123"),
			WithFlusherLogger(logger), WithFlusherMetricsProvider(instruments.provider()))
		env.flusher.cfg.MaxAttempts = 2
		env.reporter.err = errArbitrary

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		_, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		// One attempt of a budget of two: retried, not written off.
		test.SliceNotContains(t, logger.messages(logging.ErrorLevel), abandoned)
		test.SliceEmpty(t, instruments.recorded("_flushes_abandoned"))

		env.clock.advance(time.Hour)

		_, err = env.flusher.Flush(t.Context())
		must.NoError(t, err)

		// The second is the last one it had, so this is where it stops.
		test.SliceContains(t, logger.messages(logging.ErrorLevel), abandoned)
		test.Eq(t, []int64{1}, instruments.recorded("_flushes_abandoned"))
	})

	T.Run("posts several totals concurrently", func(t *testing.T) {
		t.Parallel()

		env := newTestFlusher(t, staticMapper("cus_123"))

		for _, subject := range []string{"a", "b", "c", "d", "e"} {
			entry := newEntry("req-"+subject, 1, AggregationSum)
			entry.Subject = subject

			must.NoError(t, mustRecord(t, env.store, entry))
		}

		result, err := env.flusher.Flush(t.Context())
		must.NoError(t, err)

		test.EqOp(t, 5, result.Flushed)
		test.SliceLen(t, 5, env.reporter.recorded())
	})
}

func TestFlusher_reportTimestamp(T *testing.T) {
	T.Parallel()

	env := newTestFlusher(T, staticMapper("cus_123"))

	T.Run("stamps now while the period is open", func(t *testing.T) {
		t.Parallel()

		// A provider rejects a usage record dated ahead of now, so a period still
		// running is stamped now rather than at its end.
		stamped := env.flusher.reportTimestamp(&Total{PeriodEnd: monthBounds.End})

		test.EqOp(t, baseTime, stamped)
	})

	T.Run("stamps inside a closed period", func(t *testing.T) {
		t.Parallel()

		// A record dated after the period has closed lands on the next invoice,
		// which is the wrong one.
		closed := baseTime.Add(-time.Hour)
		stamped := env.flusher.reportTimestamp(&Total{PeriodEnd: closed})

		test.EqOp(t, closed.Add(-time.Second), stamped)
	})
}

func TestFlushIdempotencyKey(T *testing.T) {
	T.Parallel()

	total := &Total{
		Subject: testSubject, Meter: testMeter,
		PeriodStart: monthBounds.Start, FlushSequence: 0,
	}

	T.Run("is stable for the same post", func(t *testing.T) {
		t.Parallel()

		// Exactly as stable as the post it identifies: a retry computes the same
		// key and the provider ignores the duplicate.
		test.EqOp(t, FlushIdempotencyKey(total), FlushIdempotencyKey(total))
	})

	T.Run("varies with the sequence", func(t *testing.T) {
		t.Parallel()

		next := *total
		next.FlushSequence = 1

		test.NotEqOp(t, FlushIdempotencyKey(total), FlushIdempotencyKey(&next))
	})

	T.Run("varies with the subject, meter, and period", func(t *testing.T) {
		t.Parallel()

		base := FlushIdempotencyKey(total)

		otherSubject := *total
		otherSubject.Subject = "account-2"
		test.NotEqOp(t, base, FlushIdempotencyKey(&otherSubject))

		otherMeter := *total
		otherMeter.Meter = "llm_tokens"
		test.NotEqOp(t, base, FlushIdempotencyKey(&otherMeter))

		otherPeriod := *total
		otherPeriod.PeriodStart = monthBounds.End
		test.NotEqOp(t, base, FlushIdempotencyKey(&otherPeriod))
	})

	T.Run("fits a provider's key limit whatever the subject is", func(t *testing.T) {
		t.Parallel()

		// Hashed rather than concatenated: a subject ID is an application's own
		// identifier and may be long or non-ASCII, and a key truncated at 255
		// bytes would collide with a different subject's.
		long := *total
		long.Subject = strings.Repeat("very-long-account-identifier-", 40)

		key := FlushIdempotencyKey(&long)

		test.Less(t, MaxIdempotencyKeyLength, len(key))
		test.StrHasPrefix(t, idempotencyKeyPrefix, key)
	})

	T.Run("handles a nil total", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", FlushIdempotencyKey(nil))
	})
}

func TestFlusher_Job(T *testing.T) {
	T.Parallel()

	env := newTestFlusher(T, staticMapper("cus_123"))

	job := env.flusher.Job(jobs.MustCron("0 * * * *"), 10*time.Minute)

	// The name is a constant because it is the job's lock key: two replicas that
	// disagree about it both run the flush, and the flush spends money.
	test.EqOp(T, DefaultFlushJobName, job.Name)
	test.EqOp(T, 10*time.Minute, job.LeaseTTL)
	must.NotNil(T, job.Run)

	must.NoError(T, mustRecord(T, env.store, newEntry("req-1", 5, AggregationSum)))
	must.NoError(T, job.Run(T.Context()))

	test.SliceLen(T, 1, env.reporter.recorded())
}

func TestFlusher_backoff(T *testing.T) {
	T.Parallel()

	env := newTestFlusher(T, staticMapper("cus_123"))

	// Full jitter rather than the equal jitter retry.Execute sleeps with: this
	// schedule is written into a row and read by a fleet, and without spreading, a
	// provider outage synchronizes every replica's retries onto one instant.
	for _, attempts := range []int{0, 1, 5, 100} {
		delay := env.flusher.backoff(attempts)

		test.Greater(T, time.Duration(0), delay, test.Sprintf("attempts %d", attempts))
		test.LessEq(T, env.flusher.cfg.Backoff.MaxDelay, delay, test.Sprintf("attempts %d", attempts))
	}

	// The floor, exercised rather than argued about. The loop above asks for a
	// positive delay from a window wide enough that it would take a billion runs
	// to draw the bottom of it, so it passes whether or not the floor is there;
	// a window one nanosecond wide leaves the draw with nowhere to go and only
	// the floor to answer with.
	//
	// Zero is the value that matters: nextFlush is a timestamp a fleet claims
	// against, and a row scheduled for the instant it was released is claimed
	// again by the same pass, spinning against whatever failed instead of
	// waiting it out.
	T.Run("floors the smallest window it can schedule", func(t *testing.T) {
		t.Parallel()

		floored := newTestFlusher(t, staticMapper("cus_123"))
		floored.flusher.cfg.Backoff = retrycfg.Config{InitialDelay: 1, Multiplier: 1, MaxDelay: 1}

		test.EqOp(t, time.Duration(1), floored.flusher.backoff(1))
		test.EqOp(t, time.Duration(1), floored.flusher.backoff(100))
	})
}

func TestTruncateError(T *testing.T) {
	T.Parallel()

	test.EqOp(T, "", truncateError(nil))
	test.EqOp(T, "boom", truncateError(platformerrors.New("boom")))

	T.Run("bounds a long rendering", func(t *testing.T) {
		t.Parallel()

		// A provider error can carry the request body back, and the request body
		// is a customer's usage.
		long := truncateError(platformerrors.New(strings.Repeat("x", maxStoredErrorLength*2)))

		test.EqOp(t, maxStoredErrorLength, len(long))
	})

	T.Run("cuts on a rune boundary", func(t *testing.T) {
		t.Parallel()

		// Half a multi-byte rune is invalid UTF-8, which some JSON encoders
		// refuse and others silently replace.
		rendered := truncateError(platformerrors.New(strings.Repeat("é", maxStoredErrorLength)))

		test.True(t, len(rendered) <= maxStoredErrorLength)
		test.True(t, strings.ToValidUTF8(rendered, "") == rendered)
	})

	T.Run("keeps a rendering that is exactly the bound", func(t *testing.T) {
		t.Parallel()

		// The bound is inclusive, and this is the one length that says so: a
		// rendering one byte shorter is kept by either reading of the check, and
		// one byte longer is cut by either.
		exact := strings.Repeat("x", maxStoredErrorLength)

		test.EqOp(t, exact, truncateError(platformerrors.New(exact)))
	})

	T.Run("a rendering that is all continuation bytes cuts to empty", func(t *testing.T) {
		t.Parallel()

		// Not a string this package produces, but one a provider SDK can hand
		// back: an error carrying a response body that was itself cut by bytes
		// somewhere upstream. The backing-up loop stops at zero for it, and
		// nothing else stops it — walking past the start would index at -1 and
		// take down a flush pass while it was already reporting a failure.
		rendered := truncateError(platformerrors.New(strings.Repeat("\x80", maxStoredErrorLength+1)))

		test.EqOp(t, "", rendered)
	})
}

// newSQLiteEnvFor rebuilds a storeEnv around an existing store's client, for the
// tests that need to count rows in tables a helper created.
func newSQLiteEnvFor(t *testing.T, store Store) *storeEnv {
	t.Helper()

	s, ok := store.(*SQLStore)
	must.True(t, ok)

	return &storeEnv{client: s.client, dialect: s.dialect}
}

// usageReporterIsSatisfied keeps the noop's interface conformance checked at
// compile time.
var _ capitalism.UsageReporter = (*recordingReporter)(nil)
