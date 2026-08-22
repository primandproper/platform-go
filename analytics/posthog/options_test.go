package posthog

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/posthog/posthog-go"
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
		})

		must.NotNil(t, o)
		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
		test.NotNil(t, o.metricsProvider)
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

func TestWithConfigModifiers(T *testing.T) {
	T.Parallel()

	T.Run("accumulates across calls and applies in order", func(t *testing.T) {
		t.Parallel()

		var applied []string

		o := newOptions([]Option{
			WithConfigModifiers(func(*posthog.Config) { applied = append(applied, "first") }),
			WithConfigModifiers(
				func(*posthog.Config) { applied = append(applied, "second") },
				func(*posthog.Config) { applied = append(applied, "third") },
			),
		})

		must.NotNil(t, o)
		must.SliceLen(t, 3, o.configModifiers)

		cfg := &posthog.Config{}
		for _, modify := range o.configModifiers {
			modify(cfg)
		}

		test.Eq(t, []string{"first", "second", "third"}, applied)
	})

	T.Run("no modifiers leaves the slice empty", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithConfigModifiers()})

		must.NotNil(t, o)
		test.SliceLen(t, 0, o.configModifiers)
	})
}
