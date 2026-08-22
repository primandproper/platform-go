package memory

import (
	"testing"
	"time"

	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newTestCache builds a cache with opts and hands back the concrete
// implementation, so a test can read the fields the options set. The options
// are applied by NewInMemoryCache itself, which is the loop under test.
func newTestCache(t *testing.T, opts ...Option) *Cache[string] {
	t.Helper()

	c, err := NewInMemoryCache[string](time.Minute, opts...)
	must.NoError(t, err)
	must.NotNil(t, c)

	return c
}

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("no options leaves every field unset", func(t *testing.T) {
		t.Parallel()

		c := newTestCache(t)

		test.Nil(t, c.logger)
		test.Nil(t, c.tracerProvider)
		test.Nil(t, c.metricsProvider)
		test.Nil(t, c.janitor)
	})

	T.Run("skips nil options", func(t *testing.T) {
		t.Parallel()

		opts := []Option{nil, WithLogger(loggingnoop.NewLogger()), nil}

		c := newTestCache(t, opts...)

		test.NotNil(t, c.logger)
	})

	T.Run("applies every option", func(t *testing.T) {
		t.Parallel()

		c := newTestCache(t,
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
		)

		test.NotNil(t, c.logger)
		test.NotNil(t, c.tracerProvider)
		test.NotNil(t, c.metricsProvider)
	})
}

func TestWithLogger(T *testing.T) {
	T.Parallel()

	T.Run("sets the logger", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, newTestCache(t, WithLogger(loggingnoop.NewLogger())).logger)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		c := newTestCache(t, WithLogger(loggingnoop.NewLogger()), WithLogger(nil))

		test.Nil(t, c.logger)
	})
}

func TestWithTracerProvider(T *testing.T) {
	T.Parallel()

	T.Run("sets the tracer provider", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, newTestCache(t, WithTracerProvider(tracingnoop.NewTracerProvider())).tracerProvider)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		c := newTestCache(t, WithTracerProvider(tracingnoop.NewTracerProvider()), WithTracerProvider(nil))

		test.Nil(t, c.tracerProvider)
	})
}

func TestWithMetricsProvider(T *testing.T) {
	T.Parallel()

	T.Run("sets the metrics provider", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, newTestCache(t, WithMetricsProvider(metricsnoop.NewMetricsProvider())).metricsProvider)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		c := newTestCache(t, WithMetricsProvider(metricsnoop.NewMetricsProvider()), WithMetricsProvider(nil))

		test.Nil(t, c.metricsProvider)
	})
}
