package authorizationcfg

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authorization/cached"
	authzdb "github.com/primandproper/platform-go/v13/authorization/database"
	"github.com/primandproper/platform-go/v13/authorization/static"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("no options leaves every backend empty", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		must.NotNil(t, o)
		test.SliceLen(t, 0, o.static)
		test.SliceLen(t, 0, o.database)
		test.SliceLen(t, 0, o.cached)
	})

	T.Run("skips nil options", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			nil,
			WithStaticOptions(static.WithLogger(loggingnoop.NewLogger())),
			nil,
		})

		must.NotNil(t, o)
		test.SliceLen(t, 1, o.static)
	})

	T.Run("collects options for every backend at once", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithStaticOptions(static.WithLogger(loggingnoop.NewLogger())),
			WithDatabaseOptions(authzdb.WithLogger(loggingnoop.NewLogger())),
			WithCachedOptions(cached.WithTTL(time.Minute)),
		})

		must.NotNil(t, o)
		test.SliceLen(t, 1, o.static)
		test.SliceLen(t, 1, o.database)
		test.SliceLen(t, 1, o.cached)
	})
}

func TestWithStaticOptions(T *testing.T) {
	T.Parallel()

	T.Run("accumulates across calls", func(t *testing.T) {
		t.Parallel()

		// static exposes only WithLogger, so the pair goes through a slice rather
		// than as repeated call arguments.
		pair := []static.Option{
			static.WithLogger(loggingnoop.NewLogger()),
			static.WithLogger(loggingnoop.NewLogger()),
		}

		o := newOptions([]Option{
			WithStaticOptions(static.WithLogger(loggingnoop.NewLogger())),
			WithStaticOptions(pair...),
		})

		must.NotNil(t, o)
		test.SliceLen(t, 3, o.static)
		test.SliceLen(t, 0, o.database)
		test.SliceLen(t, 0, o.cached)
	})

	T.Run("no arguments adds nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithStaticOptions()})

		must.NotNil(t, o)
		test.SliceLen(t, 0, o.static)
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
		test.SliceLen(t, 0, o.static)
		test.SliceLen(t, 0, o.cached)
	})

	T.Run("no arguments adds nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithDatabaseOptions()})

		must.NotNil(t, o)
		test.SliceLen(t, 0, o.database)
	})
}

func TestWithCachedOptions(T *testing.T) {
	T.Parallel()

	T.Run("accumulates across calls", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithCachedOptions(cached.WithTTL(time.Minute)),
			WithCachedOptions(
				cached.WithTTL(2*time.Minute),
				cached.WithTTL(3*time.Minute),
			),
		})

		must.NotNil(t, o)
		test.SliceLen(t, 3, o.cached)
		test.SliceLen(t, 0, o.static)
		test.SliceLen(t, 0, o.database)
	})

	T.Run("no arguments adds nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithCachedOptions()})

		must.NotNil(t, o)
		test.SliceLen(t, 0, o.cached)
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

		// Options apply in order, which is what lets a caller hand over its
		// pillars and then opt one component out.
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
