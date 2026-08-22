package ratelimiting

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// newTestLimiter builds an in-memory limiter and closes it on the way out.
//
// The close is registered rather than left to the caller because these tests
// run inside a synctest bubble, which does not close until every goroutine in
// it has exited. A sweeper that outlived its limiter would hang the test rather
// than leak quietly, so "the sweeper stops on Close" is a property every test
// here exercises, not only the one that names it.
func newTestLimiter(t *testing.T, requestsPerSec float64, burstSize int, opts ...Option) *InMemoryRateLimiter {
	t.Helper()

	limiter, err := NewInMemoryRateLimiter(requestsPerSec, burstSize, opts...)
	must.NoError(t, err)

	t.Cleanup(func() { must.NoError(t, limiter.Close()) })

	return limiter
}

// heldKeys returns the keys the limiter is physically holding.
//
// Every assertion about eviction reads the map directly. Asking through Allow
// would create whatever it asked about, so a test written that way would pass
// against a limiter that evicts nothing at all.
func heldKeys(r *InMemoryRateLimiter) []string {
	var keys []string

	r.limiters.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok {
			keys = append(keys, k)
		}

		return true
	})

	return keys
}

// held reports how many limiters are resident, and checks the counter the bound
// is enforced against still agrees with the map it is counting.
func held(t *testing.T, r *InMemoryRateLimiter) int {
	t.Helper()

	keys := heldKeys(r)
	test.EqOp(t, int64(len(keys)), r.tracked.Load(), test.Sprint("tracked count drifted from the map"))

	return len(keys)
}

// resident is held, after letting a sweep in flight finish.
//
// A bubble controls time, not scheduling: the sweeper woken by the last
// time.Sleep runs concurrently with the assertion, so without the Wait a test
// can catch it halfway through the map. Wait parks every other goroutine
// without moving the clock, which is what makes these counts exact rather than
// eventual.
func resident(t *testing.T, r *InMemoryRateLimiter) int {
	t.Helper()

	synctest.Wait()

	return held(t, r)
}

// residentKeys is heldKeys, settled the same way and ordered so it can be
// compared against a literal.
func residentKeys(r *InMemoryRateLimiter) []string {
	synctest.Wait()

	return slices.Sorted(slices.Values(heldKeys(r)))
}

// allow spends one token for key, failing the test if the limiter errors. The
// verdict is ignored: these tests are about what the limiter retains, not what
// it permits.
func allow(t *testing.T, r *InMemoryRateLimiter, key string) {
	t.Helper()

	_, err := r.Allow(context.Background(), key)
	must.NoError(t, err)
}

func TestInMemoryRateLimiter_idleEviction(T *testing.T) {
	T.Parallel()

	// 10/sec with a burst of 3 refills a full burst in 300ms, so a key is idle
	// at 600ms and swept on one of the ticks after that.
	const (
		requestsPerSec = 10.0
		burstSize      = 3
	)

	window := limiterWindow(requestsPerSec, burstSize)

	T.Run("evicts a key that stops arriving", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter := newTestLimiter(t, requestsPerSec, burstSize)

			allow(t, limiter, "gone")
			test.EqOp(t, 1, resident(t, limiter))

			time.Sleep(4 * window)

			test.EqOp(t, 0, resident(t, limiter))
		})
	})

	T.Run("keeps a key that keeps arriving", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter := newTestLimiter(t, requestsPerSec, burstSize)

			for range 20 {
				allow(t, limiter, "busy")
				time.Sleep(window / 3)
			}

			test.EqOp(t, 1, resident(t, limiter))
		})
	})

	T.Run("evicts only the keys that went quiet", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter := newTestLimiter(t, requestsPerSec, burstSize)

			allow(t, limiter, "busy")
			allow(t, limiter, "quiet")
			test.EqOp(t, 2, resident(t, limiter))

			for range 20 {
				allow(t, limiter, "busy")
				time.Sleep(window / 3)
			}

			test.Eq(t, []string{"busy"}, residentKeys(limiter))
		})
	})

	// The acceptance criterion for the leak: a process whose key space is
	// unbounded over time — one limiter per client address, forever — holds only
	// the keys that are currently live, not every key it has ever seen.
	T.Run("a long run of distinct keys does not grow the map", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter := newTestLimiter(t, requestsPerSec, burstSize)

			// One new key per window, for long enough that an un-swept map would
			// be two orders of magnitude over the ceiling asserted below.
			for i := range 500 {
				allow(t, limiter, fmt.Sprintf("key-%d", i))
				time.Sleep(window)

				// A key survives until it is idle (2 windows) and the tick after
				// that discovers it (a third), so a handful is the steady state.
				test.LessEq(t, 4, resident(t, limiter), test.Sprintf("after %d keys", i+1))
			}
		})
	})

	T.Run("counts what it evicts", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter := newTestLimiter(t, requestsPerSec, burstSize)
			idle, capacity := &countingCounter{}, &countingCounter{}
			limiter.idleEvictedCounter, limiter.capacityEvictedCounter = idle, capacity

			allow(t, limiter, "one")
			allow(t, limiter, "two")

			time.Sleep(4 * window)

			test.EqOp(t, int64(2), idle.Total())
			test.EqOp(t, int64(0), capacity.Total(), test.Sprint("nothing was evicted for capacity"))
		})
	})

	T.Run("a key that comes back is limited from a full burst", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter := newTestLimiter(t, requestsPerSec, burstSize)

			// Spend the whole burst, then leave long enough to be swept.
			for range burstSize {
				allow(t, limiter, "returning")
			}

			time.Sleep(4 * window)
			must.EqOp(t, 0, resident(t, limiter))

			// The bucket the key left behind had refilled before it was dropped,
			// so being handed a new one changes nothing it could observe.
			for range burstSize {
				allowed, err := limiter.Allow(t.Context(), "returning")
				must.NoError(t, err)
				test.True(t, allowed)
			}

			allowed, err := limiter.Allow(t.Context(), "returning")
			must.NoError(t, err)
			test.False(t, allowed)
		})
	})
}

func TestInMemoryRateLimiter_sweeperLifecycle(T *testing.T) {
	T.Parallel()

	T.Run("the sweeper stops on Close", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter, err := NewInMemoryRateLimiter(10, 3)
			must.NoError(t, err)

			_, err = limiter.Allow(t.Context(), "key")
			must.NoError(t, err)

			must.NoError(t, limiter.Close())

			// Close waits for the sweeper, so there is nothing left in the
			// bubble to advance time for. Anything still running here would
			// hang synctest.Test rather than fail it, which is the assertion.
			synctest.Wait()
			time.Sleep(time.Hour)
		})
	})

	T.Run("Close is idempotent", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter, err := NewInMemoryRateLimiter(10, 3)
			must.NoError(t, err)

			must.NoError(t, limiter.Close())
			must.NoError(t, limiter.Close())
		})
	})

	T.Run("Close forgets every key it was holding", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter := newTestLimiter(t, 10, 3)

			allow(t, limiter, "one")
			allow(t, limiter, "two")
			must.EqOp(t, 2, resident(t, limiter))

			must.NoError(t, limiter.Close())

			test.EqOp(t, 0, resident(t, limiter))
		})
	})
}

// TestInMemoryRateLimiter_injectedClock drives the sweep from a clock the test
// owns outright, rather than from the bubble's. What the bubble cannot show is
// that idleness is measured against the injected clock and nothing else: a
// limiter reading time.Now directly would pass every test above.
func TestInMemoryRateLimiter_injectedClock(T *testing.T) {
	T.Parallel()

	T.Run("sweeps when the injected clock says a key is idle", func(t *testing.T) {
		t.Parallel()

		base := time.Date(2026, time.August, 10, 0, 0, 0, 0, time.UTC)

		var now atomic.Int64
		now.Store(base.UnixNano())

		ticks := make(chan time.Time)

		mock := &clockmock.ClockMock{
			NowFunc: func() time.Time { return time.Unix(0, now.Load()) },
			NewTickerFunc: func(time.Duration) clock.Ticker {
				return &clockmock.TickerMock{
					ChanFunc: func() <-chan time.Time { return ticks },
					StopFunc: func() {},
				}
			},
		}

		limiter := newTestLimiter(t, 10, 3, WithClock(mock))

		// Handing over a tick returns as soon as the sweeper takes it, not when
		// the sweep it triggers has finished. The second send is the handshake:
		// the sweeper cannot receive it until the first sweep has returned. The
		// sweep that second tick starts may still be running during the
		// assertion, which is harmless — the clock has not moved since the
		// first one, so there is nothing left for it to find.
		sweep := func() {
			ticks <- time.Unix(0, now.Load())
			ticks <- time.Unix(0, now.Load())
		}

		allow(t, limiter, "key")
		must.EqOp(t, 1, held(t, limiter))

		// The clock has not moved, so nothing is idle however often it ticks.
		sweep()
		test.EqOp(t, 1, held(t, limiter))

		now.Store(base.Add(time.Hour).UnixNano())
		sweep()

		test.EqOp(t, 0, held(t, limiter))
	})

	T.Run("a nil clock falls back to the wall clock", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithClock(nil)})

		must.NotNil(t, o.clock)
	})
}

func TestInMemoryRateLimiter_capacityEviction(T *testing.T) {
	T.Parallel()

	T.Run("drops the least recently seen key when the bound is crossed", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter := newTestLimiter(t, 10, 3, WithMaxLimiters(4))

			// Spaced so the stamps order strictly, and well inside one window so
			// nothing is idle and only the bound can be doing the evicting.
			for _, key := range []string{"a", "b", "c", "d"} {
				allow(t, limiter, key)
				time.Sleep(time.Millisecond)
			}

			must.EqOp(t, 4, resident(t, limiter))

			allow(t, limiter, "e")

			test.Eq(t, []string{"b", "c", "d", "e"}, residentKeys(limiter))
		})
	})

	T.Run("frees headroom rather than one slot at a time", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			const bound = 32

			limiter := newTestLimiter(t, 10, 3, WithMaxLimiters(bound))

			for i := range bound + 1 {
				allow(t, limiter, fmt.Sprintf("key-%d", i))
			}

			// A pass that has to evict live limiters takes the map a sixteenth
			// below the bound, so the next inserts are free.
			test.EqOp(t, bound-bound/capacityHeadroomDivisor, resident(t, limiter))
		})
	})

	T.Run("counts capacity evictions apart from idle ones", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter := newTestLimiter(t, 10, 3, WithMaxLimiters(4))
			idle, capacity := &countingCounter{}, &countingCounter{}
			limiter.idleEvictedCounter, limiter.capacityEvictedCounter = idle, capacity

			for i := range 5 {
				allow(t, limiter, fmt.Sprintf("key-%d", i))
			}

			test.EqOp(t, int64(1), capacity.Total())
			test.EqOp(t, int64(0), idle.Total(), test.Sprint("no key had gone idle"))
		})
	})

	T.Run("a non-positive bound holds every key", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			limiter := newTestLimiter(t, 10, 3, WithMaxLimiters(0))

			for i := range 500 {
				allow(t, limiter, fmt.Sprintf("key-%d", i))
			}

			test.EqOp(t, 500, resident(t, limiter))
		})
	})

	T.Run("the default bound is in force without an option", func(t *testing.T) {
		t.Parallel()

		limiter := newTestLimiter(t, 10, 3)

		test.EqOp(t, DefaultMaxLimiters, limiter.maxLimiters)
	})

	T.Run("concurrent inserts settle within the bound", func(t *testing.T) {
		t.Parallel()

		const (
			bound   = 64
			writers = 8
			each    = 200
		)

		limiter := newTestLimiter(t, 1000, 1, WithMaxLimiters(bound))

		var wg sync.WaitGroup

		for w := range writers {
			wg.Go(func() {
				for i := range each {
					_, err := limiter.Allow(context.Background(), fmt.Sprintf("w%d-key-%d", w, i))
					must.NoError(t, err)
				}
			})
		}

		wg.Wait()

		// A pass skipped because another was already running lets the map sit
		// briefly over the bound, by at most one key per writer in flight. The
		// point is that 1600 distinct keys leave behind something bound-sized.
		//
		// Counted off the map rather than through held: the sweeper is running
		// against real time here, so the map and the counter are only equal
		// between its passes.
		test.LessEq(t, bound+writers, len(heldKeys(limiter)))
	})
}

func TestLimiterWindow(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		expected       time.Duration
		requestsPerSec float64
		burstSize      int
	}{
		"a full burst at the steady rate": {requestsPerSec: 10, burstSize: 3, expected: 300 * time.Millisecond},
		"a burst of one":                  {requestsPerSec: 4, burstSize: 1, expected: 250 * time.Millisecond},
		"a sub-unit rate":                 {requestsPerSec: 0.5, burstSize: 1, expected: 2 * time.Second},
		"a zero burst counts as one":      {requestsPerSec: 10, burstSize: 0, expected: 100 * time.Millisecond},
		"a negative burst counts as one":  {requestsPerSec: 10, burstSize: -5, expected: 100 * time.Millisecond},
		"a zero rate counts as one":       {requestsPerSec: 0, burstSize: 2, expected: 2 * time.Second},
		"a negative rate counts as one":   {requestsPerSec: -1, burstSize: 2, expected: 2 * time.Second},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, tc.expected, limiterWindow(tc.requestsPerSec, tc.burstSize))
		})
	}

	// Degenerate rates still have to yield a positive window: it is a ticker
	// interval, and time.NewTicker panics on anything else.
	T.Run("degenerate rates still yield a positive window", func(t *testing.T) {
		t.Parallel()

		for _, requestsPerSec := range []float64{math.Inf(1), math.NaN(), math.MaxFloat64} {
			test.Greater(t, time.Duration(0), limiterWindow(requestsPerSec, 1))
		}
	})
}

func TestInMemoryRateLimiter_sweepInterval(T *testing.T) {
	T.Parallel()

	T.Run("follows the window when the window is long enough", func(t *testing.T) {
		t.Parallel()

		limiter := newTestLimiter(t, 10, 3)

		test.EqOp(t, 300*time.Millisecond, limiter.sweepInterval)
		test.EqOp(t, 600*time.Millisecond, limiter.idleTTL)
	})

	// A rate this high puts the window under a millisecond, and a sweep at that
	// cadence would cost more than the memory it reclaims.
	T.Run("is floored for a very short window", func(t *testing.T) {
		t.Parallel()

		limiter := newTestLimiter(t, 100_000, 1)

		test.EqOp(t, minSweepInterval, limiter.sweepInterval)

		// The TTL is not floored with it: it says when a bucket stopped being
		// worth keeping, which the rate alone decides.
		test.Less(t, minSweepInterval, limiter.idleTTL)
	})
}

// TestNewInMemoryRateLimiter_InstrumentFailures covers the constructor's
// instrument wiring. Every instrument is built up front, so a provider that
// cannot build one has to surface at construction rather than at the first
// request or, worse, at the first sweep.
func TestNewInMemoryRateLimiter_InstrumentFailures(T *testing.T) {
	T.Parallel()

	boom := errors.New("no meter")

	counters := []string{
		"in_memory_rate_limiter_allowed",
		"in_memory_rate_limiter_rejected",
		"in_memory_rate_limiter_limiters_evicted_idle",
		"in_memory_rate_limiter_limiters_evicted_capacity",
	}

	for _, failing := range counters {
		T.Run("fails to build "+failing, func(t *testing.T) {
			t.Parallel()

			provider := &metricsmock.ProviderMock{
				NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
					if name == failing {
						return nil, boom
					}

					return nil, nil
				},
				NewInt64GaugeFunc: func(string, ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
					return nil, nil
				},
			}

			_, err := NewInMemoryRateLimiter(10, 3, WithMetricsProvider(provider))
			test.ErrorIs(t, err, boom)
		})
	}

	T.Run("fails to build the tracked limiters gauge", func(t *testing.T) {
		t.Parallel()

		provider := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return nil, nil
			},
			NewInt64GaugeFunc: func(string, ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
				return nil, boom
			},
		}

		_, err := NewInMemoryRateLimiter(10, 3, WithMetricsProvider(provider))
		test.ErrorIs(t, err, boom)
	})
}

// countingCounter records what an instrument was asked to add, so a test can
// assert a counter fired without standing up an SDK metrics pipeline.
type countingCounter struct {
	mu    sync.Mutex
	total int64
}

func (c *countingCounter) Add(_ context.Context, incr int64, _ ...metric.AddOption) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.total += incr
}

func (c *countingCounter) Total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.total
}
