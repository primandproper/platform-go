package distributedlock

import (
	"math"
	"testing"
	"time"

	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// These tests reach into scopedLocker directly. The observability options set
// unexported fields that no exported accessor reports, and grow's overflow
// guard cannot be provoked through WithLock without a backoff factor that
// NewScopedLocker rejects.

func TestScopedOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithLogger sets the logger", func(t *testing.T) {
		t.Parallel()

		s := &PollingScopedLocker{}
		WithLogger(loggingnoop.NewLogger())(s)

		test.NotNil(t, s.logger)
	})

	T.Run("WithLogger accepts nil, leaving the logger unset", func(t *testing.T) {
		t.Parallel()

		s := &PollingScopedLocker{logger: loggingnoop.NewLogger()}
		WithLogger(nil)(s)

		test.Nil(t, s.logger)
	})

	T.Run("WithTracerProvider sets the tracer provider", func(t *testing.T) {
		t.Parallel()

		s := &PollingScopedLocker{}
		WithTracerProvider(tracingnoop.NewTracerProvider())(s)

		test.NotNil(t, s.tracerProvider)
	})

	T.Run("WithTracerProvider accepts nil, leaving the provider unset", func(t *testing.T) {
		t.Parallel()

		s := &PollingScopedLocker{tracerProvider: tracingnoop.NewTracerProvider()}
		WithTracerProvider(nil)(s)

		test.Nil(t, s.tracerProvider)
	})

	T.Run("WithMetricsProvider sets the metrics provider", func(t *testing.T) {
		t.Parallel()

		s := &PollingScopedLocker{}
		WithMetricsProvider(metricsnoop.NewMetricsProvider())(s)

		test.NotNil(t, s.metricsProvider)
	})

	T.Run("every option applies independently", func(t *testing.T) {
		t.Parallel()

		s := &PollingScopedLocker{}
		for _, opt := range []ScopedOption{
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
		} {
			opt(s)
		}

		test.NotNil(t, s.logger)
		test.NotNil(t, s.tracerProvider)
		test.NotNil(t, s.metricsProvider)
	})
}

func TestScopedLocker_grow(T *testing.T) {
	T.Parallel()

	T.Run("multiplies by the backoff factor", func(t *testing.T) {
		t.Parallel()

		s := &PollingScopedLocker{pollBackoff: 2, maxPollInterval: time.Minute}

		test.EqOp(t, 2*time.Second, s.grow(time.Second))
		test.EqOp(t, 4*time.Second, s.grow(2*time.Second))
	})

	T.Run("a factor of one holds the interval fixed", func(t *testing.T) {
		t.Parallel()

		s := &PollingScopedLocker{pollBackoff: 1, maxPollInterval: time.Minute}

		test.EqOp(t, time.Second, s.grow(time.Second))
	})

	T.Run("clamps to the maximum", func(t *testing.T) {
		t.Parallel()

		s := &PollingScopedLocker{pollBackoff: 10, maxPollInterval: 5 * time.Second}

		test.EqOp(t, 5*time.Second, s.grow(time.Second))
		test.EqOp(t, 5*time.Second, s.grow(5*time.Second))
	})

	T.Run("never shrinks the interval", func(t *testing.T) {
		t.Parallel()

		// The guard's contract is that a contended wait only ever grows: a
		// shorter interval would turn a backing-off poller back into a hot one.
		// A factor below 1 is the direct way to ask for a shorter one.
		// NewScopedLocker rejects such a factor, so this reaches past it.
		s := &PollingScopedLocker{pollBackoff: 0.5, maxPollInterval: 30 * time.Second}

		got := s.grow(time.Second)
		must.EqOp(t, 30*time.Second, got)
		test.True(t, got >= time.Second)
	})

	T.Run("a huge factor still yields a bounded positive interval", func(t *testing.T) {
		t.Parallel()

		// float64(interval)*pollBackoff runs past what an int64 nanosecond count
		// can hold. Which side it lands on is architecture-dependent — arm64
		// saturates to MaxInt64, amd64 wraps to MinInt64 — so only one of grow's
		// two exits runs on any given machine. Both must give the same bounded,
		// positive answer, which is what this asserts without depending on which
		// one ran.
		s := &PollingScopedLocker{pollBackoff: math.MaxFloat64, maxPollInterval: 30 * time.Second}

		interval := 10 * time.Millisecond
		for range 100 {
			interval = s.grow(interval)
			must.EqOp(t, 30*time.Second, interval)
		}
	})
}
