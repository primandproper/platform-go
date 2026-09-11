package entitlementscfg

import (
	"testing"

	"github.com/primandproper/platform-go/v14/entitlements"

	"github.com/primandproper/primitives-go/v2/observability"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	metricsnoop "github.com/primandproper/primitives-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"

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

	T.Run("each dependency option sets the field it names", func(t *testing.T) {
		t.Parallel()

		enforcer := testEnforcer()
		flags := enabledFlags()
		assignments := newAssignmentCache(t)

		o := newOptions([]Option{
			WithEnforcer(enforcer),
			WithFeatureFlags(flags),
			WithAssignmentCache(assignments),
		})

		test.Eq(t, enforcer, o.enforcer)
		test.Eq(t, flags, o.flags)
		test.Eq(t, assignments, o.assignments)
	})

	T.Run("a nil dependency is stored as nil", func(t *testing.T) {
		t.Parallel()

		// The option records what it was given; NewChecker is what decides that
		// an absent enforcer is fine for one catalog and an error for another.
		o := newOptions([]Option{
			WithEnforcer(nil),
			WithFeatureFlags(nil),
			WithAssignmentCache(nil),
		})

		test.Nil(t, o.enforcer)
		test.Nil(t, o.flags)
		test.Nil(t, o.assignments)
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
