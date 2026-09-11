package meteringcfg

import (
	"testing"

	"github.com/primandproper/platform-go/v14/metering"

	"github.com/primandproper/primitives-go/v2/analytics"
	analyticsmock "github.com/primandproper/primitives-go/v2/analytics/mock"
	"github.com/primandproper/primitives-go/v2/cache"
	cachemock "github.com/primandproper/primitives-go/v2/cache/mock"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	metricsnoop "github.com/primandproper/primitives-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"

	"github.com/shoenig/test"
)

func TestOptions(T *testing.T) {
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

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
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

	T.Run("each dependency option sets the field it names", func(t *testing.T) {
		t.Parallel()

		var (
			reporter analytics.EventReporter           = &analyticsmock.EventReporterMock{}
			quotas   metering.QuotaSource              = metering.NewRegistryQuotaSource(metering.NewRegistry())
			totals   cache.Cache[metering.CachedTotal] = &cachemock.CacheMock[metering.CachedTotal]{}
		)

		o := newOptions([]Option{
			WithRecorderAnalytics(reporter),
			WithEnforcerQuotaSource(quotas),
			WithEnforcerCache(totals),
		})

		test.Eq(t, reporter, o.analytics)
		test.Eq(t, quotas, o.quotas)
		test.Eq(t, totals, o.totals)
	})

	T.Run("a nil dependency is stored as nil", func(t *testing.T) {
		t.Parallel()

		// The option records what it was given; the constructor is what decides
		// what an absent dependency costs.
		o := newOptions([]Option{
			WithRecorderAnalytics(nil),
			WithEnforcerQuotaSource(nil),
			WithEnforcerCache(nil),
		})

		test.Nil(t, o.analytics)
		test.Nil(t, o.quotas)
		test.Nil(t, o.totals)
	})

	T.Run("each passthrough option collects into the field it names", func(t *testing.T) {
		t.Parallel()

		// A nil entry is enough: what is under test is which slice the
		// accessor appends to, not what the underlying option does.
		test.SliceLen(t, 1, newOptions([]Option{WithStoreOptions(nil)}).store)
		test.SliceLen(t, 1, newOptions([]Option{WithRecorderOptions(nil)}).recorder)
		test.SliceLen(t, 1, newOptions([]Option{WithEnforcerOptions(nil)}).enforcer)
		test.SliceLen(t, 1, newOptions([]Option{WithFlusherOptions(nil)}).flusher)
	})
}
