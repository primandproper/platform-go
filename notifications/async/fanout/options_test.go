package fanout

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v10/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v10/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v10/observability/tracing/noop"

	"github.com/shoenig/test"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
		})

		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
		test.NotNil(t, o.metricsProvider)
	})

	T.Run("with no options", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})

	T.Run("with nil option", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil})

		test.Nil(t, o.logger)
	})
}
