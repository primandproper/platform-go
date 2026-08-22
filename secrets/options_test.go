package secrets

import (
	"context"
	"testing"
	"time"

	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("no options leaves every field unset", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		must.NotNil(t, o)
		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
		test.Nil(t, o.refreshCtx)
		test.EqOp(t, time.Duration(0), o.refreshInterval)
	})

	T.Run("skips nil options", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithLogger(loggingnoop.NewLogger()), nil})

		must.NotNil(t, o)
		test.NotNil(t, o.logger)
	})

	T.Run("applies every option", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
			WithRefresh(context.Background(), time.Minute),
		})

		must.NotNil(t, o)
		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
		test.NotNil(t, o.metricsProvider)
		test.NotNil(t, o.refreshCtx)
		test.EqOp(t, time.Minute, o.refreshInterval)
	})
}

func TestWithLogger(T *testing.T) {
	T.Parallel()

	T.Run("sets the logger", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithLogger(loggingnoop.NewLogger())})

		must.NotNil(t, o)
		test.NotNil(t, o.logger)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithLogger(loggingnoop.NewLogger()), WithLogger(nil)})

		must.NotNil(t, o)
		test.Nil(t, o.logger)
	})
}

func TestWithTracerProvider(T *testing.T) {
	T.Parallel()

	T.Run("sets the tracer provider", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithTracerProvider(tracingnoop.NewTracerProvider())})

		must.NotNil(t, o)
		test.NotNil(t, o.tracerProvider)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithTracerProvider(tracingnoop.NewTracerProvider()), WithTracerProvider(nil)})

		must.NotNil(t, o)
		test.Nil(t, o.tracerProvider)
	})
}

func TestWithMetricsProvider(T *testing.T) {
	T.Parallel()

	T.Run("sets the metrics provider", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithMetricsProvider(metricsnoop.NewMetricsProvider())})

		must.NotNil(t, o)
		test.NotNil(t, o.metricsProvider)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithMetricsProvider(metricsnoop.NewMetricsProvider()), WithMetricsProvider(nil)})

		must.NotNil(t, o)
		test.Nil(t, o.metricsProvider)
	})
}

func TestWithRefresh(T *testing.T) {
	T.Parallel()

	T.Run("sets the context and interval", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithRefresh(context.Background(), 30*time.Second)})

		must.NotNil(t, o)
		test.NotNil(t, o.refreshCtx)
		test.EqOp(t, 30*time.Second, o.refreshInterval)
	})

	T.Run("a nil context or non-positive interval configures no refresh", func(t *testing.T) {
		t.Parallel()

		for _, opt := range []Option{
			WithRefresh(nil, time.Minute), //nolint:staticcheck // SA1012: tolerating a nil context is exactly what this asserts.
			WithRefresh(context.Background(), 0),
			WithRefresh(context.Background(), -time.Second),
		} {
			o := newOptions([]Option{opt})

			must.NotNil(t, o)
			test.Nil(t, o.refreshCtx)
			test.EqOp(t, time.Duration(0), o.refreshInterval)
		}
	})
}
