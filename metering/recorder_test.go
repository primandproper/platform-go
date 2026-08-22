package metering

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/analytics"
	analyticsmock "github.com/primandproper/platform-go/v13/analytics/mock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newTestRecorder builds a recorder over a fresh store, with a stub clock.
func newTestRecorder(tb testing.TB, opts ...RecorderOption) (*DurableRecorder, Store, *stubClock) {
	tb.Helper()

	store := newSQLiteEnv(tb).newStore(tb)
	c := newStubClock()

	recorder, err := NewDurableRecorder(tb.Context(), &RecorderConfig{},
		store, newTestRegistry(tb, BehaviorBlock, 1000),
		append([]RecorderOption{WithRecorderClock(c)}, opts...)...)
	must.NoError(tb, err)

	return recorder, store, c
}

func TestNewDurableRecorder(T *testing.T) {
	T.Parallel()

	store := newSQLiteEnv(T).newStore(T)

	T.Run("refuses a nil config, store, or registry", func(t *testing.T) {
		t.Parallel()

		registry := newTestRegistry(t, BehaviorBlock, 10)

		_, err := NewDurableRecorder(t.Context(), nil, store, registry)
		test.Error(t, err)

		_, err = NewDurableRecorder(t.Context(), &RecorderConfig{}, nil, registry)
		test.ErrorIs(t, err, ErrNilStore)

		_, err = NewDurableRecorder(t.Context(), &RecorderConfig{}, store, nil)
		test.ErrorIs(t, err, ErrNilRegistry)
	})

	T.Run("fills defaults and ignores nil options", func(t *testing.T) {
		t.Parallel()

		cfg := &RecorderConfig{}

		recorder, err := NewDurableRecorder(t.Context(), cfg, store, newTestRegistry(t, BehaviorBlock, 10), nil)
		must.NoError(t, err)

		test.EqOp(t, DefaultBatchSize, recorder.cfg.BatchSize)
		test.EqOp(t, DefaultBatchSize, cfg.BatchSize)
	})

	T.Run("rejects a config that cannot be defaulted into validity", func(t *testing.T) {
		t.Parallel()

		// EnsureDefaults clamps a non-positive batch size, so the only way to a
		// validation failure is a config the caller mutated after it ran — which
		// is what this proves the constructor still catches.
		recorder, err := NewDurableRecorder(t.Context(), &RecorderConfig{BatchSize: -1},
			store, newTestRegistry(t, BehaviorBlock, 10))
		must.NoError(t, err)
		test.EqOp(t, DefaultBatchSize, recorder.cfg.BatchSize)
	})
}

func TestDurableRecorder_Record(T *testing.T) {
	T.Parallel()

	T.Run("records and folds", func(t *testing.T) {
		t.Parallel()

		recorder, store, _ := newTestRecorder(t)

		must.NoError(t, recorder.Record(t.Context(),
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 3, IdempotencyKey: "req-1"},
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 4, IdempotencyKey: "req-2"},
		))

		test.EqOp(t, int64(7), totalOf(t, store))
	})

	T.Run("stamps an absent event time from the clock", func(t *testing.T) {
		t.Parallel()

		recorder, store, _ := newTestRecorder(t)

		must.NoError(t, recorder.Record(t.Context(),
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 3, IdempotencyKey: "req-1"}))

		total, err := store.Total(t.Context(), testSubject, testMeter, monthBounds)
		must.NoError(t, err)

		test.EqOp(t, baseTime, total.LastOccurredAt)
	})

	T.Run("files usage in the period it happened in", func(t *testing.T) {
		t.Parallel()

		recorder, store, c := newTestRecorder(t)

		// The event's time, not the ingest time. A queue that redelivers an hour
		// later must still file usage in the period it happened in, or the last
		// hour of a billing period would leak into the next one every month.
		lastMonth := baseTime.AddDate(0, -1, 0)
		c.advance(time.Hour)

		must.NoError(t, recorder.Record(t.Context(),
			Usage{
				Subject: testSubject, Meter: testMeter, Quantity: 5,
				IdempotencyKey: "req-1", OccurredAt: lastMonth,
			}))

		test.EqOp(t, int64(0), totalOf(t, store))

		previous, err := store.Total(t.Context(), testSubject, testMeter, Bounds{
			Start: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			End:   monthBounds.Start,
		})
		must.NoError(t, err)
		test.EqOp(t, int64(5), previous.Quantity)
	})

	T.Run("does nothing for an empty batch", func(t *testing.T) {
		t.Parallel()

		recorder, _, _ := newTestRecorder(t)

		must.NoError(t, recorder.Record(t.Context()))
	})

	T.Run("propagates validation failures", func(t *testing.T) {
		t.Parallel()

		recorder, store, _ := newTestRecorder(t)

		// The caller's own bug, and the same on every retry — so it fails the
		// batch rather than being dropped and counted.
		test.ErrorIs(t, recorder.Record(t.Context(),
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 1}), ErrEmptyIdempotencyKey)

		test.EqOp(t, int64(0), totalOf(t, store))
	})

	T.Run("drops usage for an unregistered meter by default", func(t *testing.T) {
		t.Parallel()

		recorder, store, _ := newTestRecorder(t)

		// A deploy that adds a meter reaches the ingest path before it reaches
		// the wiring on some replica somewhere, and failing here would turn a
		// rollout into an outage on the path that was supposed to be cheap.
		must.NoError(t, recorder.Record(t.Context(),
			Usage{Subject: testSubject, Meter: "not_registered", Quantity: 5, IdempotencyKey: "req-1"},
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 3, IdempotencyKey: "req-2"},
		))

		test.EqOp(t, int64(3), totalOf(t, store))
	})

	T.Run("refuses an unregistered meter when configured to", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		recorder, err := NewDurableRecorder(t.Context(),
			&RecorderConfig{RejectUnknownMeters: true}, store, newTestRegistry(t, BehaviorBlock, 10))
		must.NoError(t, err)

		test.ErrorIs(t, recorder.Record(t.Context(),
			Usage{Subject: testSubject, Meter: "not_registered", Quantity: 5, IdempotencyKey: "req-1"}),
			ErrUnknownMeter)
	})

	T.Run("propagates a period resolution failure", func(t *testing.T) {
		t.Parallel()

		recorder, _, _ := newTestRecorder(t, WithRecorderPeriodResolver(PeriodResolverFunc(
			func(context.Context, string, Period, time.Time) (Bounds, error) {
				return Bounds{}, errArbitrary
			})))

		test.ErrorIs(t, recorder.Record(t.Context(),
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 1, IdempotencyKey: "req-1"}),
			errArbitrary)
	})

	T.Run("propagates a store failure", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		recorder, err := NewDurableRecorder(t.Context(), &RecorderConfig{},
			&recordFailingStore{Store: store}, newTestRegistry(t, BehaviorBlock, 10))
		must.NoError(t, err)

		test.ErrorIs(t, recorder.Record(t.Context(),
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 1, IdempotencyKey: "req-1"}),
			errArbitrary)
	})

	T.Run("chunks a batch larger than the configured size", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		// Chunked so one Record call cannot exceed a driver's bind-parameter
		// ceiling or hold one transaction open across an unbounded batch.
		recorder, err := NewDurableRecorder(t.Context(), &RecorderConfig{BatchSize: 2},
			store, newTestRegistry(t, BehaviorBlock, 1000), WithRecorderClock(newStubClock()))
		must.NoError(t, err)

		usages := make([]Usage, 0, 5)
		for i := range 5 {
			usages = append(usages, Usage{
				Subject: testSubject, Meter: testMeter, Quantity: 1,
				IdempotencyKey: "req-" + string(rune('a'+i)),
			})
		}

		must.NoError(t, recorder.Record(t.Context(), usages...))

		test.EqOp(t, int64(5), totalOf(t, store))
	})
}

func TestDurableRecorder_RecordTx(T *testing.T) {
	T.Parallel()

	T.Run("records in the caller's transaction", func(t *testing.T) {
		t.Parallel()

		recorder, store, _ := newTestRecorder(t)

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return recorder.RecordTx(t.Context(), q,
				Usage{Subject: testSubject, Meter: testMeter, Quantity: 6, IdempotencyKey: "req-1"})
		}))

		test.EqOp(t, int64(6), totalOf(t, store))
	})

	T.Run("rolls back with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		recorder, store, _ := newTestRecorder(t)

		// The usage and the work it describes are one fact: a crash between them
		// leaves work committed that nobody was billed for.
		test.ErrorIs(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			must.NoError(t, recorder.RecordTx(t.Context(), q,
				Usage{Subject: testSubject, Meter: testMeter, Quantity: 6, IdempotencyKey: "req-1"}))

			return errArbitrary
		}), errArbitrary)

		test.EqOp(t, int64(0), totalOf(t, store))
	})

	T.Run("refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		recorder, _, _ := newTestRecorder(t)

		test.ErrorIs(t, recorder.RecordTx(t.Context(), nil,
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 1, IdempotencyKey: "req-1"}),
			ErrNilExecutor)
	})
}

func TestDurableRecorder_Analytics(T *testing.T) {
	T.Parallel()

	T.Run("emits one event per record with flattened dimensions", func(t *testing.T) {
		t.Parallel()

		var (
			mu     sync.Mutex
			events []map[string]any
		)

		reporter := &analyticsmock.EventReporterMock{
			EventOccurredFunc: func(_ context.Context, _, _ string, properties map[string]any) error {
				mu.Lock()
				defer mu.Unlock()

				events = append(events, properties)

				return nil
			},
		}

		recorder, _, _ := newTestRecorder(t, WithRecorderAnalytics(reporter))

		must.NoError(t, recorder.Record(t.Context(), Usage{
			Subject: testSubject, Meter: testMeter, Quantity: 3, IdempotencyKey: "req-1",
			Dimensions: map[string]string{"model": "opus"},
		}))

		must.SliceLen(t, 1, events)
		test.Eq(t, any(testMeter), events[0]["meter"])
		test.Eq(t, any(int64(3)), events[0]["quantity"])
		// Flattened and prefixed, so a dimension called "meter" cannot overwrite
		// the meter, and most warehouses can index it.
		test.EqOp(t, "opus", events[0]["dimension_model"])
	})

	T.Run("swallows an analytics failure", func(t *testing.T) {
		t.Parallel()

		reporter := &analyticsmock.EventReporterMock{
			EventOccurredFunc: func(context.Context, string, string, map[string]any) error {
				return errArbitrary
			},
		}

		logger := newRecordingLogger()
		recorder, store, _ := newTestRecorder(t, WithRecorderAnalytics(reporter), WithRecorderLogger(logger))

		// Analytics is a side channel: an ingest path that failed because a
		// warehouse was unreachable would be a metering outage caused by a system
		// nobody bills from.
		must.NoError(t, recorder.Record(t.Context(),
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 3, IdempotencyKey: "req-1"}))

		test.EqOp(t, int64(3), totalOf(t, store))

		// Swallowed is not the same as unreported. A warehouse that has stopped
		// receiving events is invisible from the warehouse's end — nothing there
		// knows what it should have been sent — so the ingest side saying so is
		// the only notice anybody gets.
		errors := logger.at(logging.ErrorLevel)
		must.SliceLen(t, 1, errors)
		test.ErrorIs(t, errors[0].err, errArbitrary)
		test.EqOp(t, testMeter, errors[0].values[meterKey])
	})

	T.Run("reports nothing when the warehouse accepts the event", func(t *testing.T) {
		t.Parallel()

		reporter := &analyticsmock.EventReporterMock{
			EventOccurredFunc: func(context.Context, string, string, map[string]any) error {
				return nil
			},
		}

		logger := newRecordingLogger()
		recorder, _, _ := newTestRecorder(t, WithRecorderAnalytics(reporter), WithRecorderLogger(logger))

		must.NoError(t, recorder.Record(t.Context(),
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 3, IdempotencyKey: "req-1"}))

		test.SliceEmpty(t, logger.at(logging.ErrorLevel))
	})

	T.Run("emits nothing when a batch was entirely duplicate", func(t *testing.T) {
		t.Parallel()

		var calls int

		reporter := &analyticsmock.EventReporterMock{
			EventOccurredFunc: func(context.Context, string, string, map[string]any) error {
				calls++

				return nil
			},
		}

		recorder, _, _ := newTestRecorder(t, WithRecorderAnalytics(reporter))

		usage := Usage{Subject: testSubject, Meter: testMeter, Quantity: 3, IdempotencyKey: "req-1"}

		must.NoError(t, recorder.Record(t.Context(), usage))
		must.NoError(t, recorder.Record(t.Context(), usage))

		test.EqOp(t, 1, calls)
	})

	T.Run("counts what the store accepted and what it had already", func(t *testing.T) {
		t.Parallel()

		instruments := newRecordingInstruments()
		recorder, _, _ := newTestRecorder(t, WithRecorderMetricsProvider(instruments.provider()))

		usage := Usage{Subject: testSubject, Meter: testMeter, Quantity: 3, IdempotencyKey: "req-1"}

		must.NoError(t, recorder.Record(t.Context(), usage))

		// The first pass is all new: one record accepted, nothing seen before.
		test.Eq(t, []int64{1}, instruments.recorded("_usage_recorded"))
		test.SliceEmpty(t, instruments.recorded("_usage_duplicates"))

		must.NoError(t, recorder.Record(t.Context(), usage))

		// The redelivery is all duplicate, and reports itself as such. Reporting
		// a zero on the other counter instead would be worse than reporting
		// nothing: a graph of ingest that never goes quiet cannot be told from
		// one whose writes have stopped mattering.
		test.Eq(t, []int64{1}, instruments.recorded("_usage_recorded"))
		test.Eq(t, []int64{1}, instruments.recorded("_usage_duplicates"))

		// The quantity counter is fed per entry either way — it measures what
		// the caller reported, not what the store kept.
		test.Eq(t, []int64{3, 3}, instruments.recorded("_usage_quantity"))
	})

	T.Run("ignores a nil reporter", func(t *testing.T) {
		t.Parallel()

		recorder, store, _ := newTestRecorder(t, WithRecorderAnalytics(nil))

		must.NoError(t, recorder.Record(t.Context(),
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 3, IdempotencyKey: "req-1"}))

		test.EqOp(t, int64(3), totalOf(t, store))
		test.Nil(t, recorder.analytics)
	})

	T.Run("uses the documented event name", func(t *testing.T) {
		t.Parallel()

		var saw string

		reporter := &analyticsmock.EventReporterMock{
			EventOccurredFunc: func(_ context.Context, event, _ string, _ map[string]any) error {
				saw = event

				return nil
			},
		}

		recorder, _, _ := newTestRecorder(t, WithRecorderAnalytics(reporter))

		must.NoError(t, recorder.Record(t.Context(),
			Usage{Subject: testSubject, Meter: testMeter, Quantity: 1, IdempotencyKey: "req-1"}))

		test.EqOp(t, AnalyticsEvent, saw)
	})
}

func TestRecorderOptions(T *testing.T) {
	T.Parallel()

	store := newSQLiteEnv(T).newStore(T)
	registry := newTestRegistry(T, BehaviorBlock, 10)

	T.Run("ignores nil dependencies", func(t *testing.T) {
		t.Parallel()

		// A nil clock or resolver leaves the constructor's default in place
		// rather than producing a recorder that panics on its first call.
		recorder, err := NewDurableRecorder(t.Context(), &RecorderConfig{}, store, registry,
			WithRecorderClock(nil), WithRecorderPeriodResolver(nil))
		must.NoError(t, err)

		test.NotNil(t, recorder.clock)
		test.NotNil(t, recorder.resolver)
	})

	T.Run("accepts every observability option", func(t *testing.T) {
		t.Parallel()

		recorder, err := NewDurableRecorder(t.Context(), &RecorderConfig{}, store, registry,
			WithRecorderLogger(nil),
			WithRecorderTracerProvider(nil),
			WithRecorderMetricsProvider(nil),
		)
		must.NoError(t, err)
		must.NotNil(t, recorder)
	})
}

func TestChunks(T *testing.T) {
	T.Parallel()

	T.Run("yields bounded slices", func(t *testing.T) {
		t.Parallel()

		var got [][]int
		for chunk := range chunks([]int{1, 2, 3, 4, 5}, 2) {
			got = append(got, chunk)
		}

		test.Eq(t, [][]int{{1, 2}, {3, 4}, {5}}, got)
	})

	T.Run("yields nothing for an empty slice", func(t *testing.T) {
		t.Parallel()

		var got [][]int
		for chunk := range chunks([]int(nil), 2) {
			got = append(got, chunk)
		}

		test.SliceEmpty(t, got)
	})

	T.Run("stops early", func(t *testing.T) {
		t.Parallel()

		var count int
		for range chunks([]int{1, 2, 3, 4, 5, 6}, 2) {
			count++

			break
		}

		test.EqOp(t, 1, count)
	})
}

// analyticsReporterIsSatisfied keeps the mock's interface conformance checked at
// compile time, so a change to analytics.EventReporter surfaces here rather than
// in a confusing failure inside a subtest.
var _ analytics.EventReporter = (*analyticsmock.EventReporterMock)(nil)
