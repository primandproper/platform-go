package billing

import (
	"testing"

	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	metricsnoop "github.com/primandproper/primitives-go/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/observability/tracing/noop"

	"github.com/shoenig/test"
)

// apply builds the store the options would have configured, without opening a
// database. Every option here is a field write; what a store does with the
// fields is the store suite's business.
func apply(opts ...SQLStoreOption) *SQLStore {
	s := &SQLStore{}
	for _, opt := range opts {
		opt(s)
	}

	return s
}

func TestSQLStoreOptions(T *testing.T) {
	T.Parallel()

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()

		s := apply(
			WithStoreLogger(logger),
			WithStoreTracerProvider(tracerProvider),
			WithStoreMetricsProvider(metricsProvider),
		)

		test.Eq(t, logger, s.logger)
		test.Eq(t, tracerProvider, s.tracerProvider)
		test.Eq(t, metricsProvider, s.metricsProvider)
	})

	T.Run("naming none of them leaves all three absent", func(t *testing.T) {
		t.Parallel()

		// A caller wanting no observability names no observability, which is why
		// these are options rather than parameters.
		s := apply()

		test.Nil(t, s.logger)
		test.Nil(t, s.tracerProvider)
		test.Nil(t, s.metricsProvider)
	})

	T.Run("WithStorePillars supplies all three at once", func(t *testing.T) {
		t.Parallel()

		pillars := &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		}

		s := apply(WithStorePillars(pillars))

		test.Eq(t, pillars.Logger, s.logger)
		test.Eq(t, pillars.TracerProvider, s.tracerProvider)
		test.Eq(t, pillars.MetricsProvider, s.metricsProvider)
	})

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		s := apply(WithStorePillars(nil))

		test.Nil(t, s.logger)
		test.Nil(t, s.tracerProvider)
		test.Nil(t, s.metricsProvider)
	})

	T.Run("a later option overrides what the pillars supplied", func(t *testing.T) {
		t.Parallel()

		// Options apply in order, which is what lets a caller hand over its
		// pillars and then leave this one store unmetered.
		s := apply(
			WithStorePillars(&observability.Pillars{
				Logger:          loggingnoop.NewLogger(),
				TracerProvider:  tracingnoop.NewTracerProvider(),
				MetricsProvider: metricsnoop.NewMetricsProvider(),
			}),
			WithStoreMetricsProvider(nil),
		)

		test.NotNil(t, s.logger)
		test.Nil(t, s.metricsProvider)
	})
}
