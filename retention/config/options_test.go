package retentioncfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/retention"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("newOptions ignores nil entries", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithLogger(logging.EnsureLogger(nil)), nil})
		must.NotNil(t, o.logger)
	})

	T.Run("the individual options each set one dependency", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithLogger(logging.EnsureLogger(nil)),
			WithTracerProvider(tracing.EnsureTracerProvider(nil)),
			WithMetricsProvider(metrics.EnsureMetricsProvider(nil)),
		})

		must.NotNil(t, o.logger)
		must.NotNil(t, o.tracerProvider)
		must.NotNil(t, o.metricsProvider)
	})

	T.Run("WithPillars sets all three", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(&observability.Pillars{
			Logger:          logging.EnsureLogger(nil),
			TracerProvider:  tracing.EnsureTracerProvider(nil),
			MetricsProvider: metrics.EnsureMetricsProvider(nil),
		})})

		must.NotNil(t, o.logger)
		must.NotNil(t, o.tracerProvider)
		must.NotNil(t, o.metricsProvider)
	})

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(nil)})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})

	T.Run("options apply in order, so one can be overridden after the pillars", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithPillars(&observability.Pillars{
				Logger:          logging.EnsureLogger(nil),
				MetricsProvider: metrics.EnsureMetricsProvider(nil),
			}),
			WithMetricsProvider(nil),
		})

		must.NotNil(t, o.logger)
		test.Nil(t, o.metricsProvider)
	})

	T.Run("WithSweeperOptions accumulates", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithSweeperOptions(retention.WithSweeperLogger(nil)),
			WithSweeperOptions(retention.WithSweeperLogger(nil), retention.WithSweeperActor(audit.Actor{})),
		})

		test.SliceLen(t, 3, o.sweeper)
	})
}
