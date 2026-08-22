package eventcapture

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

type testEvent struct {
	At   time.Time
	Name string
}

// mustRecorder builds a Recorder, failing the test if its instruments cannot
// be constructed.
func mustRecorder[E any](tb testing.TB, sink Sink, opts ...Option) *Recorder[E] {
	tb.Helper()

	r, err := NewRecorder[E](sink, opts...)
	must.NoError(tb, err)

	return r
}

// recordingSink is a threadsafe in-memory Sink that counts every Flush. Tests
// read the count after a synctest.Wait, which parks the flusher without moving
// the clock — so the count is exactly the flushes that were actually due.
type recordingSink struct {
	records []any
	mu      sync.Mutex
	flushes int
	closed  bool
}

func newRecordingSink() *recordingSink {
	return &recordingSink{}
}

func (s *recordingSink) Write(record any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)

	return nil
}

func (s *recordingSink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushes++

	return nil
}

func (s *recordingSink) flushCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.flushes
}

func (s *recordingSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true

	return nil
}

func (s *recordingSink) snapshot() []any {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]any, len(s.records))
	copy(out, s.records)

	return out
}

func (s *recordingSink) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closed
}

// erroringSink fails every call, standing in for a capture destination that
// has gone bad underneath a running Recorder. Per the package contract none of
// these errors ever reach a caller, so tests assert on the counts instead.
type erroringSink struct {
	mu      sync.Mutex
	writes  int
	flushes int
	closes  int
}

var errSinkBroken = platformerrors.New("sink is broken")

func (s *erroringSink) Write(any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++

	return errSinkBroken
}

func (s *erroringSink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushes++

	return errSinkBroken
}

func (s *erroringSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++

	return errSinkBroken
}

func (s *erroringSink) counts() (writes, flushes, closes int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.writes, s.flushes, s.closes
}

// discardHistogram satisfies metrics.Float64Histogram for tests that care only
// about the counters. The metrics mock package generates no histogram double.
type discardHistogram struct{}

func (discardHistogram) Record(context.Context, float64, ...metric.RecordOption) {}

// countingProvider is a metrics.Provider whose counters tally the increments
// they are handed, keyed by instrument name.
type countingProvider struct {
	*metricsmock.ProviderMock

	totals map[string]int64
	mu     sync.Mutex
}

func newCountingProvider() *countingProvider {
	p := &countingProvider{totals: map[string]int64{}}
	p.ProviderMock = &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			return &metricsmock.Int64CounterMock{
				AddFunc: func(_ context.Context, incr int64, _ ...metric.AddOption) {
					p.mu.Lock()
					defer p.mu.Unlock()
					p.totals[name] += incr
				},
			}, nil
		},
		NewFloat64HistogramFunc: func(string, ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
			return discardHistogram{}, nil
		},
	}

	return p
}

func (p *countingProvider) total(name string) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.totals[name]
}

var testStart = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// flushInterval is the tick cadence the periodic-flush tests configure. Bubble
// time makes it free, so the value is arbitrary — but the tests step to a
// nanosecond either side of it, so they fail if the ticker ignores it.
const flushInterval = time.Second

// The Recorder tests run inside synctest bubbles: the flusher goroutine, the
// event buffer, and the flush ticker all live in the bubble, so the default
// clock rides bubble time and a Run/Close handshake that fails to complete is
// reported as a deadlock instead of hanging on a timeout.

func TestNewRecorder(T *testing.T) {
	T.Parallel()

	T.Run("rejects a nil sink", func(t *testing.T) {
		t.Parallel()

		_, err := NewRecorder[testEvent](nil)
		test.Error(t, err)
	})

	T.Run("nil options are skipped", func(t *testing.T) {
		t.Parallel()

		r, err := NewRecorder[testEvent](newRecordingSink(),
			nil,
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
			WithClock(nil),
			WithBufferSize(0),
			WithFlushInterval(0),
		)
		must.NoError(t, err)
		// The no-op option values left the defaults in place.
		test.EqOp(t, DefaultBufferSize, cap(r.events))
		test.EqOp(t, DefaultFlushInterval, r.flushInterval)
	})

	// Option carries no E, so a hook built for another event type type-checks.
	// It has to be caught here rather than silently dropped, which would leave a
	// composition that looks wired up and records nothing.
	T.Run("rejects hooks built for a different event type", func(t *testing.T) {
		t.Parallel()

		type otherEvent struct{ Other string }

		_, err := NewRecorder[testEvent](newRecordingSink(),
			WithTransform(func(*otherEvent) any { return nil }))
		test.ErrorIs(t, err, ErrEventTypeMismatch)

		_, err = NewRecorder[testEvent](newRecordingSink(),
			WithObserver(func(*otherEvent) {}))
		test.ErrorIs(t, err, ErrEventTypeMismatch)
	})

	T.Run("accepts hooks for its own event type without a type argument", func(t *testing.T) {
		t.Parallel()

		var observed int

		r, err := NewRecorder[testEvent](newRecordingSink(),
			WithObserver(func(*testEvent) { observed++ }),
			WithTransform(func(e *testEvent) any { return e.Name }),
		)
		must.NoError(t, err)
		must.NotNil(t, r.observe)
		must.NotNil(t, r.transform)

		r.observe(&testEvent{})
		test.EqOp(t, 1, observed)
		test.EqOp(t, any("x"), r.transform(&testEvent{Name: "x"}))
	})

	T.Run("an instrument that cannot be built fails construction", func(t *testing.T) {
		t.Parallel()

		// Each instrument is built in order, so failing the Nth counter walks
		// the constructor's error paths one at a time.
		counters := []string{
			"eventcapture_records_written",
			"eventcapture_records_dropped",
			"eventcapture_aggregation_overflow",
			"eventcapture_sink_errors",
		}

		for i, failing := range counters {
			t.Run(failing, func(t *testing.T) {
				t.Parallel()

				mp := &metricsmock.ProviderMock{
					NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
						if name == failing {
							return nil, platformerrors.New("instrument unavailable")
						}

						return &metricsmock.Int64CounterMock{}, nil
					},
				}

				_, err := NewRecorder[testEvent](newRecordingSink(), WithMetricsProvider(mp))
				must.Error(t, err)
				test.SliceLen(t, i+1, mp.NewInt64CounterCalls())
			})
		}

		t.Run("eventcapture_flush_latency_ms", func(t *testing.T) {
			t.Parallel()

			mp := &metricsmock.ProviderMock{
				NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
					return &metricsmock.Int64CounterMock{}, nil
				},
				NewFloat64HistogramFunc: func(string, ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
					return nil, platformerrors.New("instrument unavailable")
				},
			}

			_, err := NewRecorder[testEvent](newRecordingSink(), WithMetricsProvider(mp))
			must.Error(t, err)
			test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
		})
	})
}

func TestRecorder_SinkFailures(T *testing.T) {
	T.Parallel()

	T.Run("write, flush, and close errors are counted, not returned", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			sink := &erroringSink{}
			mp := newCountingProvider()

			r := mustRecorder[testEvent](t, sink,
				WithLogger(loggingnoop.NewLogger()),
				WithMetricsProvider(mp),
			)

			go r.Run()

			r.Record(&testEvent{Name: "doomed"})

			// Close drains: the event's write fails, then the final flush
			// fails, then the sink's own Close fails — and Close still reports
			// success, because the requests behind these events are long gone.
			must.NoError(t, r.Close(t.Context()))

			writes, flushes, closes := sink.counts()
			test.EqOp(t, 1, writes)
			test.EqOp(t, 1, flushes)
			test.EqOp(t, 1, closes)

			test.EqOp(t, int64(3), mp.total("eventcapture_sink_errors"))
			test.EqOp(t, int64(0), mp.total("eventcapture_records_written"))
		})
	})

	T.Run("dropped events are reported on the next flush", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			mp := newCountingProvider()

			// Nothing is consuming yet, so the buffer fills for real.
			r := mustRecorder[testEvent](t, newRecordingSink(),
				WithBufferSize(1),
				WithLogger(loggingnoop.NewLogger()),
				WithMetricsProvider(mp),
			)

			r.Record(&testEvent{Name: "kept"})
			r.Record(&testEvent{Name: "dropped-a"})
			r.Record(&testEvent{Name: "dropped-b"})
			must.EqOp(t, uint64(2), r.Dropped())

			go r.Run()

			must.NoError(t, r.Close(t.Context()))

			test.EqOp(t, int64(2), mp.total("eventcapture_records_dropped"))
		})
	})

	T.Run("Close gives up when its context expires", func(t *testing.T) {
		t.Parallel()

		// No Run, so nothing ever drains and closes the done channel.
		r := mustRecorder[testEvent](t, newRecordingSink())

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		test.Error(t, r.Close(ctx))
	})
}

func TestRecorder_WithClock(T *testing.T) {
	T.Parallel()

	T.Run("the flush ticker rides the supplied clock", func(t *testing.T) {
		t.Parallel()

		ticks := make(chan time.Time, 1)
		var stops int
		var mu sync.Mutex

		c := &clockmock.ClockMock{
			NowFunc: func() time.Time { return testStart },
			NewTickerFunc: func(time.Duration) clock.Ticker {
				return &clockmock.TickerMock{
					ChanFunc: func() <-chan time.Time { return ticks },
					StopFunc: func() {
						mu.Lock()
						defer mu.Unlock()
						stops++
					},
				}
			},
		}

		sink := newRecordingSink()

		// Periodic flushes report themselves here, so the test can wait for the
		// tick to be consumed rather than racing Close against it.
		ticked := make(chan time.Time, 1)
		r := mustRecorder[testEvent](t, sink,
			WithClock(c),
			WithOnFlush(func(now time.Time, final bool, _ func(any)) {
				if !final {
					ticked <- now
				}
			}),
		)

		done := make(chan struct{})
		go func() {
			defer close(done)
			r.Run()
		}()

		// A tick the test controls outright: no wall time and no bubble needed.
		ticks <- testStart
		flushedAt := <-ticked

		must.NoError(t, r.Close(t.Context()))
		<-done

		// The flusher timestamped the flush from the injected clock, not the wall.
		test.EqOp(t, testStart, flushedAt)
		// The tick's flush plus the final drain flush.
		test.EqOp(t, 2, sink.flushCount())
		test.SliceLen(t, 1, c.NewTickerCalls())

		mu.Lock()
		defer mu.Unlock()
		test.EqOp(t, 1, stops)
	})
}

func TestRecorder_RecordAndClose(T *testing.T) {
	T.Parallel()

	T.Run("events flow to the sink and Close drains and closes", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			sink := newRecordingSink()
			r := mustRecorder[testEvent](t, sink)

			go r.Run()

			r.Record(&testEvent{Name: "one"})
			r.Record(&testEvent{Name: "two"})

			must.NoError(t, r.Close(t.Context()))

			records := sink.snapshot()
			must.SliceLen(t, 2, records)
			first, ok := records[0].(*testEvent)
			must.True(t, ok)
			test.EqOp(t, "one", first.Name)
			test.True(t, sink.isClosed())
			test.EqOp(t, uint64(0), r.Dropped())
		})
	})

	T.Run("Close is idempotent", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			sink := newRecordingSink()
			r := mustRecorder[testEvent](t, sink)

			go r.Run()

			must.NoError(t, r.Close(t.Context()))
			must.NoError(t, r.Close(t.Context()))
		})
	})

	T.Run("a full buffer drops and counts instead of blocking", func(t *testing.T) {
		t.Parallel()

		sink := newRecordingSink()
		// No Run(): nothing consumes, so the buffer genuinely fills.
		r := mustRecorder[testEvent](t, sink, WithBufferSize(1))

		r.Record(&testEvent{Name: "kept"})
		r.Record(&testEvent{Name: "dropped-a"})
		r.Record(&testEvent{Name: "dropped-b"})

		test.EqOp(t, uint64(2), r.Dropped())
	})

	T.Run("WithoutRawRecords suppresses per-event writes", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			sink := newRecordingSink()
			var observed int
			r := mustRecorder[testEvent](t, sink,
				WithoutRawRecords(),
				WithObserver(func(*testEvent) { observed++ }),
			)

			go r.Run()

			r.Record(&testEvent{Name: "only-observed"})

			must.NoError(t, r.Close(t.Context()))
			must.SliceLen(t, 0, sink.snapshot())
			test.EqOp(t, 1, observed)
		})
	})

	T.Run("WithTransform projects events before the sink", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			sink := newRecordingSink()
			r := mustRecorder[testEvent](t, sink,
				WithTransform(func(ev *testEvent) any {
					return map[string]string{"name": ev.Name}
				}),
			)

			go r.Run()

			r.Record(&testEvent{Name: "projected"})

			must.NoError(t, r.Close(t.Context()))

			records := sink.snapshot()
			must.SliceLen(t, 1, records)
			projected, ok := records[0].(map[string]string)
			must.True(t, ok)
			test.EqOp(t, "projected", projected["name"])
		})
	})
}

func TestRecorder_PeriodicFlush(T *testing.T) {
	T.Parallel()

	T.Run("ticks run the flush hook and flush the sink", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			sink := newRecordingSink()

			var hookCalls int
			var sawFinal bool
			var mu sync.Mutex

			r := mustRecorder[testEvent](t, sink,
				WithFlushInterval(flushInterval),
				WithOnFlush(func(_ time.Time, final bool, emit func(any)) {
					mu.Lock()
					defer mu.Unlock()
					hookCalls++
					if final {
						sawFinal = true
					} else {
						emit("tick-record")
					}
				}),
			)

			go r.Run()

			// Close is idempotent, so the explicit one below still owns the
			// assertions; this only unwinds the flusher if one of them fails
			// first, turning a deadlocked bubble into a plain failure.
			defer func() { _ = r.Close(t.Context()) }()

			// A nanosecond short of the interval. Wait parks the flusher without
			// moving the clock, so the tick is pinned to its deadline rather than
			// to whenever the bubble next goes idle.
			time.Sleep(flushInterval - time.Nanosecond)
			synctest.Wait()
			must.EqOp(t, 0, sink.flushCount())

			// Crossing the deadline fires exactly one tick.
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			must.EqOp(t, 1, sink.flushCount())

			must.NoError(t, r.Close(t.Context()))

			mu.Lock()
			defer mu.Unlock()
			// One periodic tick plus the final drain flush.
			test.EqOp(t, 2, hookCalls)
			test.True(t, sawFinal)

			records := sink.snapshot()
			must.SliceLen(t, 1, records)
			test.Eq(t, any("tick-record"), records[0])
		})
	})

	T.Run("WithOverflowSource is drained on every flush", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			sink := newRecordingSink()

			var mu sync.Mutex
			var polls int

			r := mustRecorder[testEvent](t, sink,
				WithFlushInterval(flushInterval),
				WithOverflowSource(func() uint64 {
					mu.Lock()
					defer mu.Unlock()
					polls++

					return 3
				}),
			)

			go r.Run()

			defer func() { _ = r.Close(t.Context()) }()

			time.Sleep(flushInterval - time.Nanosecond)
			synctest.Wait()
			must.EqOp(t, 0, sink.flushCount())

			time.Sleep(time.Nanosecond)
			synctest.Wait()
			must.EqOp(t, 1, sink.flushCount())

			must.NoError(t, r.Close(t.Context()))

			mu.Lock()
			defer mu.Unlock()
			// The periodic tick plus the final drain flush: an Aggregator's
			// overflow is reported and reset on both, so a shutdown never strands
			// the last window's discards.
			test.EqOp(t, 2, polls)
		})
	})
}

func TestAggregator(T *testing.T) {
	T.Parallel()

	type counts struct {
		total int
	}

	cmpKeys := func(a, b string) int {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	}

	// AggregatorOption carries no K, so a comparison over another key type
	// type-checks. Dropping it silently would leave Flush non-deterministic in a
	// way nothing surfaces, so NewAggregator panics instead.
	T.Run("panics on a key order for a different key type", func(t *testing.T) {
		t.Parallel()

		defer func() {
			r := recover()
			must.NotNil(t, r)
		}()

		_ = NewAggregator[string, counts](time.Minute, 0, WithKeyOrder(func(a, b int) int { return a - b }))
		t.Fatal("expected a panic")
	})

	T.Run("folds observations into time buckets", func(t *testing.T) {
		t.Parallel()

		agg := NewAggregator[string, counts](time.Minute, 0, WithKeyOrder(cmpKeys))

		inc := func(c *counts) { c.total++ }
		agg.Observe("a", testStart, inc)
		agg.Observe("a", testStart.Add(30*time.Second), inc) // same bucket
		agg.Observe("a", testStart.Add(90*time.Second), inc) // next bucket
		agg.Observe("b", testStart, inc)

		// Only the first window has closed by 1m30s.
		buckets := agg.Flush(testStart.Add(90*time.Second), false)
		must.SliceLen(t, 2, buckets)
		test.EqOp(t, "a", buckets[0].Key)
		test.EqOp(t, 2, buckets[0].Counts.total)
		test.EqOp(t, "b", buckets[1].Key)
		test.EqOp(t, 1, buckets[1].Counts.total)
		test.EqOp(t, testStart, buckets[0].Start)

		// The drain path emits the still-open bucket too.
		rest := agg.Flush(testStart.Add(90*time.Second), true)
		must.SliceLen(t, 1, rest)
		test.EqOp(t, testStart.Add(time.Minute), rest[0].Start)

		// Everything was removed as it flushed.
		test.SliceLen(t, 0, agg.Flush(testStart.Add(time.Hour), true))
	})

	T.Run("bounded cell map drops and counts overflow", func(t *testing.T) {
		t.Parallel()

		agg := NewAggregator[string, counts](time.Minute, 2)

		inc := func(c *counts) { c.total++ }
		agg.Observe("a", testStart, inc)
		agg.Observe("b", testStart, inc)
		agg.Observe("c", testStart, inc) // over the cap: dropped
		agg.Observe("a", testStart, inc) // existing cell: still folded

		test.EqOp(t, uint64(1), agg.TakeOverflow())
		test.EqOp(t, uint64(0), agg.TakeOverflow())

		buckets := agg.Flush(testStart.Add(time.Hour), true)
		must.SliceLen(t, 2, buckets)
	})

	T.Run("a non-positive bucket falls back to one minute", func(t *testing.T) {
		t.Parallel()

		agg := NewAggregator[string, counts](0, 0, nil)

		agg.Observe("a", testStart, func(c *counts) { c.total++ })

		buckets := agg.Flush(testStart.Add(time.Hour), false)
		must.SliceLen(t, 1, buckets)
		test.EqOp(t, time.Minute, buckets[0].Size)
	})

	T.Run("without WithKeyOrder same-window buckets still all come out", func(t *testing.T) {
		t.Parallel()

		agg := NewAggregator[string, counts](time.Minute, 0)

		inc := func(c *counts) { c.total++ }
		agg.Observe("a", testStart, inc)
		agg.Observe("b", testStart, inc)

		// Same window, no key comparison configured: order is unspecified, so
		// the assertion is on the set rather than the sequence.
		buckets := agg.Flush(testStart.Add(time.Hour), false)
		must.SliceLen(t, 2, buckets)
		seen := map[string]int{}
		for _, b := range buckets {
			seen[b.Key] = b.Counts.total
		}
		test.Eq(t, map[string]int{"a": 1, "b": 1}, seen)
	})
}
