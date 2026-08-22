package saga

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
// the behavior worth having, because a Worker that started without
// saga_instances_stuck would run indefinitely with the one counter anybody
// alerts on silently absent. These tests assert that each of those checks is
// actually wired, one instrument at a time.
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

func TestWorker_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_steps_completed",
		serviceName + "_step_failures",
		serviceName + "_instances_completed",
		serviceName + "_compensations_started",
		serviceName + "_instances_compensated",
		serviceName + "_instances_stuck",
		serviceName + "_claim_errors",
		serviceName + "_lock_contended",
		serviceName + "_step_latency_ms",
		serviceName + "_advance_latency_ms",
	}

	for _, instrument := range instruments {
		T.Run(instrument, func(t *testing.T) {
			t.Parallel()

			store := newSQLiteEnv(t).newStore(t)
			registry := registryWith(t, "orders", noopStep("one"))

			_, err := NewWorker(t.Context(), &WorkerConfig{}, store, registry, newScopedLocker(t),
				WithWorkerMetricsProvider(failingInstrumentProvider(instrument)))

			must.ErrorIs(t, err, errInstrument)
			test.StrContains(t, err.Error(), "creating")
		})
	}
}

func TestRunner_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_instances_started",
		serviceName + "_instances_resumed",
	}

	for _, instrument := range instruments {
		T.Run(instrument, func(t *testing.T) {
			t.Parallel()

			store := newSQLiteEnv(t).newStore(t)

			_, err := NewRunner[testState](store, NewRegistry(),
				WithRunnerMetricsProvider(failingInstrumentProvider(instrument)))

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
