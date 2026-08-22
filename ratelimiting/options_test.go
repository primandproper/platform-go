package ratelimiting

import (
	"testing"

	"github.com/primandproper/platform-go/v13/clock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("no options leaves the observability seams unset and the rest defaulted", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		must.NotNil(t, o)
		test.Nil(t, o.metricsProvider)

		// Absent means noop for observability, and means the documented default
		// for the two knobs that keep the limiter's memory bounded — neither has
		// a sensible "off".
		test.NotNil(t, o.clock)
		test.EqOp(t, DefaultMaxLimiters, o.maxLimiters)
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
			WithClock(clock.NewClock()),
			WithMaxLimiters(7),
		})

		must.NotNil(t, o)
		test.NotNil(t, o.metricsProvider)
		test.NotNil(t, o.clock)
		test.EqOp(t, 7, o.maxLimiters)
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
