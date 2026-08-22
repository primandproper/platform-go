package shredding

import (
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test"
	"go.opentelemetry.io/otel/metric"
)

// errInstrument is what the failing provider returns for the one instrument
// under test.
var errInstrument = platformerrors.New("instrument unavailable")

// failingInstrumentProvider serves every instrument except the named one.
//
// Both constructors here register their instruments up front, so a misconfigured
// meter fails the wiring rather than the first erasure. These tests assert each
// of those checks is actually wired: a missed `if err != nil` is invisible until
// the day a meter is misconfigured in production, which is the day somebody is
// trying to find out whether a shred happened.
func failingInstrumentProvider(failing string) *metricsmock.ProviderMock {
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
	}
}

func TestKeys_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_keys_minted",
		serviceName + "_keys_shredded",
		serviceName + "_key_unwraps",
		serviceName + "_cache_hits",
		serviceName + "_cache_misses",
		serviceName + "_invalidations_broadcast",
		serviceName + "_invalidations_broadcast_failures",
		serviceName + "_invalidations_applied",
		serviceName + "_cached_keys",
	}

	for _, name := range instruments {
		T.Run("refuses to build without "+name, func(t *testing.T) {
			t.Parallel()

			keys, err := NewKeys(newSQLiteEnv(t).newStore(t), newTestWrapper(t),
				WithMetricsProvider(failingInstrumentProvider(name)))
			test.Nil(t, keys)
			test.ErrorIs(t, err, errInstrument)
		})
	}
}

func TestInvalidationHandler_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_invalidations_received",
		serviceName + "_invalidations_rejected",
	}

	for _, name := range instruments {
		T.Run("refuses to build without "+name, func(t *testing.T) {
			t.Parallel()

			handler, err := NewInvalidationHandler(&recordingInvalidator{},
				WithInvalidationMetricsProvider(failingInstrumentProvider(name)))
			test.Nil(t, handler)
			test.ErrorIs(t, err, errInstrument)
		})
	}
}

func TestSQLStore_InstrumentFailures(T *testing.T) {
	T.Parallel()

	T.Run("refuses to build without the mint conflict counter", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(newSQLiteEnv(t).client,
			WithStoreMetricsProvider(failingInstrumentProvider(storeName+"_mint_conflicts")))
		test.Nil(t, store)
		test.ErrorIs(t, err, errInstrument)
	})
}
