package authzdbcfg

import (
	"testing"
	"time"

	authzdb "github.com/primandproper/platform-go/v14/authorization/database"

	"github.com/primandproper/primitives-go/authorization/cached"
	authorizationcfg "github.com/primandproper/primitives-go/authorization/config"
	"github.com/primandproper/primitives-go/authorization/static"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	metricsnoop "github.com/primandproper/primitives-go/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("no options leaves every passthrough empty", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		must.NotNil(t, o)
		test.SliceLen(t, 0, o.database)
		test.SliceLen(t, 0, o.resolver)
	})

	T.Run("skips nil options", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			nil,
			WithDatabaseOptions(authzdb.WithLogger(loggingnoop.NewLogger())),
			nil,
		})

		must.NotNil(t, o)
		test.SliceLen(t, 1, o.database)
	})
}

func TestWithDatabaseOptions(T *testing.T) {
	T.Parallel()

	T.Run("accumulates across calls", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithDatabaseOptions(authzdb.WithLogger(loggingnoop.NewLogger())),
			WithDatabaseOptions(
				authzdb.WithTracerProvider(tracingnoop.NewTracerProvider()),
				authzdb.WithMetricsProvider(metricsnoop.NewMetricsProvider()),
			),
		})

		must.NotNil(t, o)
		test.SliceLen(t, 3, o.database)
		test.SliceLen(t, 0, o.resolver)
	})

	T.Run("no arguments adds nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithDatabaseOptions()})

		must.NotNil(t, o)
		test.SliceLen(t, 0, o.database)
	})
}

func TestWithResolverOptions(T *testing.T) {
	T.Parallel()

	T.Run("accumulates across calls", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithResolverOptions(authorizationcfg.WithStaticOptions(static.WithLogger(loggingnoop.NewLogger()))),
			WithResolverOptions(
				authorizationcfg.WithCachedOptions(cached.WithTTL(time.Minute)),
				authorizationcfg.WithCachedOptions(cached.WithTTL(2*time.Minute)),
			),
		})

		must.NotNil(t, o)
		test.SliceLen(t, 3, o.resolver)
		test.SliceLen(t, 0, o.database)
	})

	T.Run("no arguments adds nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithResolverOptions()})

		must.NotNil(t, o)
		test.SliceLen(t, 0, o.resolver)
	})

	// The passthrough is the only route the primitive half's options have, so
	// what is rendered for it carries the caller's entries last.
	T.Run("renders after this package's own observability", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithLogger(loggingnoop.NewLogger()),
			WithResolverOptions(authorizationcfg.WithStaticOptions()),
		})

		test.SliceLen(t, 4, o.resolverOptions())
	})
}

func TestOptions_observability(T *testing.T) {
	T.Parallel()

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()

		o := newOptions([]Option{
			WithLogger(logger),
			WithTracerProvider(tracerProvider),
			WithMetricsProvider(metricsProvider),
		})

		test.Eq(t, logger, o.logger)
		test.Eq(t, tracerProvider, o.tracerProvider)
		test.Eq(t, metricsProvider, o.metricsProvider)
	})

	T.Run("WithPillars supplies every dependency this package takes", func(t *testing.T) {
		t.Parallel()

		pillars := &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		}

		o := newOptions([]Option{WithPillars(pillars)})

		test.Eq(t, pillars.Logger, o.logger)
		test.Eq(t, pillars.TracerProvider, o.tracerProvider)
		test.Eq(t, pillars.MetricsProvider, o.metricsProvider)
	})

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(nil)})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})

	T.Run("a later option overrides what the pillars supplied", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithPillars(&observability.Pillars{
				Logger:          loggingnoop.NewLogger(),
				TracerProvider:  tracingnoop.NewTracerProvider(),
				MetricsProvider: metricsnoop.NewMetricsProvider(),
			}),
			WithMetricsProvider(nil),
		})

		test.Nil(t, o.metricsProvider)
		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
	})
}
