package dataprivacy

import (
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// errInstrument is what the failing provider returns for the one instrument
// under test.
var errInstrument = platformerrors.New("instrument unavailable")

// failingInstrumentProvider builds a metrics.Provider that serves every
// instrument except the named one, which it refuses.
//
// Each constructor in this package registers its instruments up front so that a
// misconfigured meter fails construction rather than the first cycle — which is
// the behavior worth having, because a Worker that started without its counters
// would run indefinitely with no way to see that it was running. These tests
// assert that each of those checks is actually wired, one instrument at a time:
// a missed `if err != nil` there is invisible until the day a meter is
// misconfigured in production.
func failingInstrumentProvider(failing string) *metricsmock.ProviderMock {
	// Delegated to rather than reimplemented, so only the failure is a double.
	noop := metrics.EnsureMetricsProvider(nil)

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if name == failing {
				return nil, errInstrument
			}

			return noop.NewInt64Counter(name, opts...)
		},
		NewInt64GaugeFunc: func(name string, opts ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
			if name == failing {
				return nil, errInstrument
			}

			return noop.NewInt64Gauge(name, opts...)
		},
		NewFloat64HistogramFunc: func(name string, opts ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
			if name == failing {
				return nil, errInstrument
			}

			return noop.NewFloat64Histogram(name, opts...)
		},
	}
}

func TestFulfiller_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_requests_completed",
		serviceName + "_requests_failed",
		serviceName + "_sections_collected",
		serviceName + "_section_failures",
		serviceName + "_exports_partial",
		serviceName + "_notification_failures",
		serviceName + "_requests_stopped",
		serviceName + "_rows_erased",
		serviceName + "_fulfillment_latency_ms",
		serviceName + "_collector_latency_ms",
		serviceName + "_artifact_bytes",
	}

	for _, instrument := range instruments {
		T.Run(instrument, func(t *testing.T) {
			t.Parallel()

			env := newSQLiteEnv(t)

			registry := NewRegistry()
			must.NoError(t, registry.RegisterEraser("identity", countingEraser(0, 0, nil, nil)))

			_, err := NewFulfiller(t.Context(), &FulfillerConfig{}, env.newStore(t), registry,
				WithFulfillerMetricsProvider(failingInstrumentProvider(instrument)))

			must.ErrorIs(t, err, errInstrument)
			test.StrContains(t, err.Error(), "creating")
		})
	}
}

func TestService_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_requests_submitted",
		serviceName + "_requests_confirmed",
		serviceName + "_requests_cancelled",
		serviceName + "_artifact_downloads",
	}

	for _, instrument := range instruments {
		T.Run(instrument, func(t *testing.T) {
			t.Parallel()

			env := newSQLiteEnv(t)

			_, err := NewService(t.Context(), &ServiceConfig{}, env.newStore(t), newStubOperations(),
				WithServiceMetricsProvider(failingInstrumentProvider(instrument)))

			must.ErrorIs(t, err, errInstrument)
			test.StrContains(t, err.Error(), "creating")
		})
	}
}

func TestSweeper_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_artifacts_expired",
		serviceName + "_erasures_lapsed",
		serviceName + "_requests_reaped",
		serviceName + "_artifact_delete_errors",
		serviceName + "_requests_overdue",
		serviceName + "_sweep_latency_ms",
	}

	for _, instrument := range instruments {
		T.Run(instrument, func(t *testing.T) {
			t.Parallel()

			env := newSQLiteEnv(t)

			_, err := NewSweeper(t.Context(), &SweeperConfig{}, env.newStore(t),
				WithSweeperMetricsProvider(failingInstrumentProvider(instrument)))

			must.ErrorIs(t, err, errInstrument)
			test.StrContains(t, err.Error(), "creating")
		})
	}
}

func TestSQLStore_InstrumentFailures(T *testing.T) {
	T.Parallel()

	T.Run(storeName+"_guard_misses", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		_, err := NewSQLStore(env.client,
			WithStoreMetricsProvider(failingInstrumentProvider(storeName+"_guard_misses")))

		must.ErrorIs(t, err, errInstrument)
		test.StrContains(t, err.Error(), "creating")
	})
}
