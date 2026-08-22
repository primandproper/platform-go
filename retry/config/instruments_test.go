package retrycfg

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	"github.com/primandproper/platform-go/v13/retry"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// errInstrument is what the failing provider returns for the one instrument
// under test.
var errInstrument = platformerrors.New("instrument unavailable")

func failingInstrumentProvider(failing string) *metricsmock.ProviderMock {
	noop := metrics.EnsureMetricsProvider(nil)

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if name == failing {
				return nil, errInstrument
			}

			return noop.NewInt64Counter(name, opts...)
		},
	}
}

func TestNewExponentialBackoffPolicy_InstrumentFailures(T *testing.T) {
	T.Parallel()

	for _, name := range []string{serviceName + "_attempts", serviceName + "_exhaustions"} {
		T.Run("refuses to build without "+name, func(t *testing.T) {
			t.Parallel()

			policy, err := NewExponentialBackoffPolicy(Config{},
				WithMetricsProvider(failingInstrumentProvider(name)))
			test.Nil(t, policy)
			test.ErrorIs(t, err, errInstrument)
		})
	}

	T.Run("and neither does NewPolicy", func(t *testing.T) {
		t.Parallel()

		policy, err := NewPolicy(t.Context(), validConfig(),
			WithMetricsProvider(failingInstrumentProvider(serviceName+"_attempts")))
		// The interface must be nil, not a non-nil interface holding a nil
		// pointer, or a caller testing the result against nil finds a value that
		// panics on first use.
		test.Nil(t, policy)
		test.ErrorIs(t, err, errInstrument)
	})
}

// countingInstruments records what a policy emitted.
type countingInstruments struct {
	counts map[string]int
	mu     sync.Mutex
}

func newCountingInstruments() *countingInstruments {
	return &countingInstruments{counts: map[string]int{}}
}

func (c *countingInstruments) provider() *metricsmock.ProviderMock {
	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			return &metricsmock.Int64CounterMock{
				AddFunc: func(context.Context, int64, ...metric.AddOption) {
					c.mu.Lock()
					defer c.mu.Unlock()

					c.counts[name]++
				},
			}, nil
		},
	}
}

func (c *countingInstruments) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.counts[name]
}

func TestExponentialBackoffPolicy_ReportsAttemptsAndExhaustion(T *testing.T) {
	T.Parallel()

	T.Run("an exhausted loop counts every attempt and one exhaustion", func(t *testing.T) {
		t.Parallel()

		instruments := newCountingInstruments()

		policy := newPolicy(t, Config{MaxAttempts: 3, InitialDelay: time.Nanosecond, MaxDelay: time.Nanosecond},
			WithMetricsProvider(instruments.provider()))

		failure := platformerrors.New("connection refused")

		err := policy.Execute(t.Context(), func(context.Context) error { return failure })

		must.Error(t, err)
		test.EqOp(t, 3, instruments.count(serviceName+"_attempts"))
		test.EqOp(t, 1, instruments.count(serviceName+"_exhaustions"))
	})

	T.Run("a loop that succeeds exhausts nothing", func(t *testing.T) {
		t.Parallel()

		instruments := newCountingInstruments()

		policy := newPolicy(t, Config{MaxAttempts: 3, InitialDelay: time.Nanosecond, MaxDelay: time.Nanosecond},
			WithMetricsProvider(instruments.provider()))

		attempts := 0

		must.NoError(t, policy.Execute(t.Context(), func(context.Context) error {
			attempts++
			if attempts < 2 {
				return platformerrors.New("transient")
			}

			return nil
		}))

		test.EqOp(t, 2, instruments.count(serviceName+"_attempts"))
		test.EqOp(t, 0, instruments.count(serviceName+"_exhaustions"))
	})
}

func TestExponentialBackoffPolicy_CarriesAttemptCount(T *testing.T) {
	T.Parallel()

	T.Run("an exhausted loop says how many attempts it took", func(t *testing.T) {
		t.Parallel()

		policy := newPolicy(t, Config{MaxAttempts: 4, InitialDelay: time.Nanosecond, MaxDelay: time.Nanosecond})

		failure := platformerrors.New("connection refused")

		err := policy.Execute(t.Context(), func(context.Context) error { return failure })

		must.Error(t, err)
		// The last error is still in the chain, so nothing a caller could match
		// against before stopped matching.
		test.ErrorIs(t, err, failure)
		test.ErrorIs(t, err, retry.ErrExhausted)

		attempts, ok := retry.Attempts(err)
		must.True(t, ok)
		test.EqOp(t, uint(4), attempts)
	})

	T.Run("an unretryable failure is not exhaustion", func(t *testing.T) {
		t.Parallel()

		policy := newPolicy(t, Config{MaxAttempts: 4, InitialDelay: time.Nanosecond, MaxDelay: time.Nanosecond})

		failure := platformerrors.New("bad request")

		err := policy.Execute(t.Context(), func(context.Context) error { return retry.Unretryable(failure) })

		must.Error(t, err)
		test.ErrorIs(t, err, failure)

		_, ok := retry.Attempts(err)
		test.False(t, ok)
	})
}

func TestExponentialBackoffPolicy_SleepsOnTheInjectedClock(T *testing.T) {
	T.Parallel()

	T.Run("every wait goes through the clock it was given", func(t *testing.T) {
		t.Parallel()

		var (
			mu     sync.Mutex
			slept  []time.Duration
			ticker = clock.NewClock()
		)

		injected := &clockmock.ClockMock{
			NowFunc:   ticker.Now,
			SinceFunc: ticker.Since,
			SleepFunc: func(_ context.Context, d time.Duration) error {
				mu.Lock()
				defer mu.Unlock()

				slept = append(slept, d)

				return nil
			},
		}

		policy := newPolicy(t, Config{
			MaxAttempts:  3,
			InitialDelay: time.Hour,
			MaxDelay:     4 * time.Hour,
			Multiplier:   2,
		}, WithClock(injected))

		err := policy.Execute(t.Context(), func(context.Context) error {
			return platformerrors.New("transient")
		})
		must.Error(t, err)

		// Two waits for three attempts, on the configured schedule and not on
		// wall time — the test would otherwise take three hours.
		mu.Lock()
		defer mu.Unlock()

		test.Eq(t, []time.Duration{time.Hour, 2 * time.Hour}, slept)
	})

	T.Run("a clock that reports a done context stops the loop", func(t *testing.T) {
		t.Parallel()

		injected := &clockmock.ClockMock{
			SleepFunc: func(context.Context, time.Duration) error { return context.Canceled },
		}

		policy := newPolicy(t, Config{MaxAttempts: 5, InitialDelay: time.Hour, MaxDelay: time.Hour},
			WithClock(injected))

		failure := platformerrors.New("transient")
		attempts := 0

		err := policy.Execute(t.Context(), func(context.Context) error {
			attempts++

			return failure
		})

		test.ErrorIs(t, err, failure)
		test.EqOp(t, 1, attempts)
		// Cut short is not exhausted: the loop had attempts left.
		_, ok := retry.Attempts(err)
		test.False(t, ok)
	})
}
