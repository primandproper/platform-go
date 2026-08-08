package entitlementscfg

import (
	"testing"

	"github.com/primandproper/platform-go/v10/entitlements"
	"github.com/primandproper/platform-go/v10/observability"
	loggingnoop "github.com/primandproper/platform-go/v10/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v10/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v10/observability/tracing/noop"

	"github.com/shoenig/test"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("each option sets its own field", func(t *testing.T) {
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

	T.Run("a nil option is ignored", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil})

		test.Nil(t, o.logger)
	})

	T.Run("WithPillars sets all three", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(&observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		})})

		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
		test.NotNil(t, o.metricsProvider)
	})

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(nil)})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})

	T.Run("options apply in order, so one can be overridden after Pillars", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithPillars(&observability.Pillars{
				Logger:          loggingnoop.NewLogger(),
				MetricsProvider: metricsnoop.NewMetricsProvider(),
			}),
			WithMetricsProvider(nil),
		})

		test.NotNil(t, o.logger)
		test.Nil(t, o.metricsProvider)
	})

	T.Run("checker options accumulate", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithCheckerOptions(entitlements.WithLogger(loggingnoop.NewLogger())),
			WithCheckerOptions(entitlements.WithEnforcer(testEnforcer())),
		})

		test.SliceLen(t, 2, o.checker)
	})
}
