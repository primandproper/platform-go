package oauth2clients

import (
	"testing"

	"github.com/primandproper/platform-go/v14/observability"
	"github.com/primandproper/platform-go/v14/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v14/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v14/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v14/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// applyStoreOptions builds the store the options would have configured, without
// opening a database. Every option is a field write; what the store does with
// the fields is the store suite's business.
func applyStoreOptions(opts ...SQLStoreOption) *SQLStore {
	s := &SQLStore{}
	for _, opt := range opts {
		opt(s)
	}

	return s
}

// applyServiceOptions is the same for the service.
func applyServiceOptions(opts ...ServiceOption) *Service {
	s := &Service{}
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

		s := applyStoreOptions(
			WithTablePrefix("ddb"),
			WithStoreLogger(logger),
			WithStoreTracerProvider(tracerProvider),
		)

		test.EqOp(t, "ddb", s.prefix)
		test.Eq(t, logger, s.logger)
		test.Eq(t, tracerProvider, s.tracerProvider)
	})

	T.Run("naming none of them leaves them absent", func(t *testing.T) {
		t.Parallel()

		// A caller wanting no observability names none, which is why these are
		// options rather than parameters.
		s := applyStoreOptions()

		test.Nil(t, s.logger)
		test.Nil(t, s.tracerProvider)
	})

	T.Run("WithStorePillars supplies the two this store has", func(t *testing.T) {
		t.Parallel()

		// Two of three, deliberately: this store ships no instruments, and
		// taking a metrics provider would be a wiring call that appears to have
		// configured something. The service's WithServicePillars takes all
		// three, which is where the instruments actually are.
		pillars := &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		}

		s := applyStoreOptions(WithStorePillars(pillars))

		test.Eq(t, pillars.Logger, s.logger)
		test.Eq(t, pillars.TracerProvider, s.tracerProvider)
	})

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		s := applyStoreOptions(WithStorePillars(nil))

		test.Nil(t, s.logger)
		test.Nil(t, s.tracerProvider)
	})

	T.Run("a later option overrides what the pillars supplied", func(t *testing.T) {
		t.Parallel()

		// Options apply in order, which is what lets a caller hand over their
		// pillars and then leave this one component untraced.
		s := applyStoreOptions(
			WithStorePillars(&observability.Pillars{
				Logger:         loggingnoop.NewLogger(),
				TracerProvider: tracingnoop.NewTracerProvider(),
			}),
			WithStoreTracerProvider(nil),
		)

		test.NotNil(t, s.logger)
		test.Nil(t, s.tracerProvider)
	})
}

func TestServiceOptions(T *testing.T) {
	T.Parallel()

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()

		s := applyServiceOptions(
			WithServiceLogger(logger),
			WithServiceTracerProvider(tracerProvider),
			WithServiceMetricsProvider(metricsProvider),
		)

		test.Eq(t, logger, s.logger)
		test.Eq(t, tracerProvider, s.tracerProvider)
		test.Eq(t, metricsProvider, s.metricsProvider)
	})

	T.Run("WithServicePillars supplies all three at once", func(t *testing.T) {
		t.Parallel()

		pillars := &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		}

		s := applyServiceOptions(WithServicePillars(pillars))

		test.Eq(t, pillars.Logger, s.logger)
		test.Eq(t, pillars.TracerProvider, s.tracerProvider)
		test.Eq(t, pillars.MetricsProvider, s.metricsProvider)
	})

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		s := applyServiceOptions(WithServicePillars(nil))

		test.Nil(t, s.logger)
		test.Nil(t, s.tracerProvider)
		test.Nil(t, s.metricsProvider)
	})

	T.Run("a later option overrides what the pillars supplied", func(t *testing.T) {
		t.Parallel()

		s := applyServiceOptions(
			WithServicePillars(&observability.Pillars{
				Logger:          loggingnoop.NewLogger(),
				TracerProvider:  tracingnoop.NewTracerProvider(),
				MetricsProvider: metricsnoop.NewMetricsProvider(),
			}),
			WithServiceMetricsProvider(nil),
		)

		test.NotNil(t, s.logger)
		test.NotNil(t, s.tracerProvider)
		test.Nil(t, s.metricsProvider)
	})

	T.Run("WithHooks and WithCredentialGenerator refuse to unset themselves", func(t *testing.T) {
		t.Parallel()

		// Both defaults are the safe answer — NoopHooks, and a generator reading
		// crypto/rand — so a nil argument leaves them in place rather than
		// installing a nil the next call would panic on. A consumer building an
		// option list conditionally passes whatever they resolved.
		configured := applyServiceOptions(
			WithHooks(NoopHooks{}),
			WithCredentialGenerator(generateCredentials),
		)
		must.NotNil(t, configured.hooks)
		must.NotNil(t, configured.generate)

		unset := applyServiceOptions(
			WithHooks(NoopHooks{}),
			WithCredentialGenerator(generateCredentials),
			WithHooks(nil),
			WithCredentialGenerator(nil),
		)

		test.NotNil(t, unset.hooks)
		test.NotNil(t, unset.generate)
	})
}
