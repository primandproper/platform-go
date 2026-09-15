package webauthnsessions

import (
	"testing"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	metricsmock "github.com/primandproper/primitives-go/v2/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// errInstrument is what the failing provider returns for the one instrument
// under test.
var errInstrument = platformerrors.New("instrument unavailable")

// recordingInstrumentProvider serves every instrument and remembers what it was
// asked for.
func recordingInstrumentProvider(names *[]string) *metricsmock.ProviderMock {
	noop := metrics.EnsureMetricsProvider(nil)

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			*names = append(*names, name)

			return noop.NewInt64Counter(name, opts...)
		},
	}
}

// failingInstrumentProvider serves every instrument except the named one.
func failingInstrumentProvider(failing string) *metricsmock.ProviderMock {
	noop := metrics.EnsureMetricsProvider(nil)

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if name == failing {
				return nil, errInstrument
			}

			return noop.NewInt64Counter(name, opts...)
		},
	}
}

// The names are spelled out here rather than composed from serviceName, which
// is the whole point of the test: an instrument name is a string somebody else's
// dashboard is keyed on, so it is not something this package gets to change by
// renaming a constant. Composing them from serviceName would agree with any
// value it ever held. The package moved paths once, and the constant moved with
// it — this is the test that makes the next such move a decision.
func TestNewSessionStore_InstrumentNames(T *testing.T) {
	T.Parallel()

	var names []string

	_, err := NewSessionStore(&Config{}, newTestClient(T),
		WithMetricsProvider(recordingInstrumentProvider(&names)))
	must.NoError(T, err)

	test.Eq(T, []string{
		"webauthnsessions_database_rows_swept",
		"webauthnsessions_database_sweep_errors",
	}, names)
}

// The store registers its instruments up front, so a misconfigured meter fails
// the wiring rather than the first sweep. These assert each of those checks is
// wired: a missed `if err != nil` is invisible until the day a meter is
// misconfigured, which is the day somebody wants to know whether the table is
// still being reclaimed.
func TestNewSessionStore_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_rows_swept",
		serviceName + "_sweep_errors",
	}

	for _, name := range instruments {
		T.Run("refuses to build without "+name, func(t *testing.T) {
			t.Parallel()

			store, err := NewSessionStore(&Config{}, newTestClient(t),
				WithMetricsProvider(failingInstrumentProvider(name)))
			test.Nil(t, store)
			test.ErrorIs(t, err, errInstrument)
		})
	}
}
