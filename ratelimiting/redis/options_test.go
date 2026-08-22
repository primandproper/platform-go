package redis

import (
	"testing"

	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("no options leaves every field unset", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		must.NotNil(t, o)
		test.Nil(t, o.metricsProvider)
	})

	T.Run("skips nil options", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithMetricsProvider(metricsnoop.NewMetricsProvider()), nil})

		must.NotNil(t, o)
		test.NotNil(t, o.metricsProvider)
	})

	T.Run("applies every option", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
		})

		must.NotNil(t, o)
		test.NotNil(t, o.metricsProvider)
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
