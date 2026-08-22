package metering

import (
	"testing"

	capitalismnoop "github.com/primandproper/platform-go/v13/capitalism/noop"
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
// the behavior worth having, because a Flusher that started without its counters
// would post usage indefinitely with no way to see that it was. These tests
// assert that each of those checks is actually wired, one instrument at a time: a
// missed `if err != nil` there is invisible until the day a meter is
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

func TestDurableRecorder_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_usage_recorded",
		serviceName + "_usage_duplicates",
		serviceName + "_usage_dropped",
		serviceName + "_usage_quantity",
		serviceName + "_ingest_latency_ms",
	}

	for _, instrument := range instruments {
		T.Run(instrument, func(t *testing.T) {
			t.Parallel()

			env := newSQLiteEnv(t)

			_, err := NewDurableRecorder(t.Context(), &RecorderConfig{},
				env.newStore(t), newTestRegistry(t, BehaviorBlock, 10),
				WithRecorderMetricsProvider(failingInstrumentProvider(instrument)))

			must.ErrorIs(t, err, errInstrument)
			test.StrContains(t, err.Error(), "creating")
		})
	}
}

func TestQuotaEnforcer_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_checks",
		serviceName + "_consumes",
		serviceName + "_denied",
		serviceName + "_overage",
		serviceName + "_stale_checks",
		serviceName + "_cache_errors",
		serviceName + "_fail_opens",
		serviceName + "_check_latency_ms",
		serviceName + "_consume_latency_ms",
	}

	for _, instrument := range instruments {
		T.Run(instrument, func(t *testing.T) {
			t.Parallel()

			env := newSQLiteEnv(t)

			_, err := NewQuotaEnforcer(t.Context(), &EnforcerConfig{},
				env.newStore(t), newTestRegistry(t, BehaviorBlock, 10),
				WithEnforcerMetricsProvider(failingInstrumentProvider(instrument)))

			must.ErrorIs(t, err, errInstrument)
			test.StrContains(t, err.Error(), "creating")
		})
	}
}

func TestFlusher_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_flushes",
		serviceName + "_flushes_skipped",
		serviceName + "_flush_failures",
		serviceName + "_flushed_quantity",
		serviceName + "_flushes_abandoned",
		serviceName + "_events_reaped",
		serviceName + "_flush_backlog",
		serviceName + "_provider_post_latency_ms",
		serviceName + "_flush_pass_latency_ms",
	}

	for _, instrument := range instruments {
		T.Run(instrument, func(t *testing.T) {
			t.Parallel()

			env := newSQLiteEnv(t)

			_, err := NewFlusher(t.Context(), &FlusherConfig{}, env.newStore(t),
				staticMapper("si_123"), capitalismnoop.NewUsageReporter(),
				WithFlusherMetricsProvider(failingInstrumentProvider(instrument)))

			must.ErrorIs(t, err, errInstrument)
			test.StrContains(t, err.Error(), "creating")
		})
	}
}
