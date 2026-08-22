package healthcheck

import (
	"context"
	"sync"
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
		NewInt64GaugeFunc: func(name string, opts ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
			if name == failing {
				return nil, errInstrument
			}

			return noop.NewInt64Gauge(name, opts...)
		},
	}
}

func TestNewRegistry_InstrumentFailures(T *testing.T) {
	T.Parallel()

	instruments := []string{
		serviceName + "_component_transitions",
		serviceName + "_components_down",
	}

	for _, name := range instruments {
		T.Run("refuses to build without "+name, func(t *testing.T) {
			t.Parallel()

			registry, err := NewRegistry(WithMetricsProvider(failingInstrumentProvider(name)))
			test.Nil(t, registry)
			test.ErrorIs(t, err, errInstrument)
		})
	}
}

// recordingInstruments counts what the registry emitted, which is the only way
// to tell "reported the transition" from "answered the probe correctly".
type recordingInstruments struct {
	downs       []int64
	transitions int
	mu          sync.Mutex
}

func (r *recordingInstruments) provider() *metricsmock.ProviderMock {
	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			return &metricsmock.Int64CounterMock{
				AddFunc: func(context.Context, int64, ...metric.AddOption) {
					r.mu.Lock()
					defer r.mu.Unlock()

					r.transitions++
				},
			}, nil
		},
		NewInt64GaugeFunc: func(string, ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
			return &recordingGauge{owner: r}, nil
		},
	}
}

func (r *recordingInstruments) counts() (transitions int, downs []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.transitions, append([]int64(nil), r.downs...)
}

type recordingGauge struct {
	owner *recordingInstruments
}

func (g *recordingGauge) Record(_ context.Context, value int64, _ ...metric.RecordOption) {
	g.owner.mu.Lock()
	defer g.owner.mu.Unlock()

	g.owner.downs = append(g.owner.downs, value)
}

func TestCheckerRegistry_ReportsTransitionsOnce(T *testing.T) {
	T.Parallel()

	T.Run("a steady component is reported once, and a change once more", func(t *testing.T) {
		t.Parallel()

		instruments := &recordingInstruments{}

		registry, err := NewRegistry(WithMetricsProvider(instruments.provider()))
		must.NoError(t, err)

		component := &mockChecker{name: "database"}
		registry.Register(component)

		// The first observation counts: a service should say what state it found
		// each dependency in, not only speak up once one of them breaks.
		registry.CheckAll(t.Context())
		registry.CheckAll(t.Context())

		transitions, downs := instruments.counts()
		test.EqOp(t, 1, transitions)
		test.Eq(t, []int64{0, 0}, downs)

		// Going down is one transition, however many probes observe it.
		failure := platformerrors.New("connection refused")
		component.checkFn = func(context.Context) error { return failure }

		registry.CheckAll(t.Context())
		registry.CheckAll(t.Context())
		registry.CheckAll(t.Context())

		transitions, downs = instruments.counts()
		test.EqOp(t, 2, transitions)
		test.Eq(t, []int64{0, 0, 1, 1, 1}, downs)

		// And so is coming back.
		component.checkFn = nil

		registry.CheckAll(t.Context())

		transitions, downs = instruments.counts()
		test.EqOp(t, 3, transitions)
		test.Eq(t, []int64{0, 0, 1, 1, 1, 0}, downs)
	})

	T.Run("each component transitions independently", func(t *testing.T) {
		t.Parallel()

		instruments := &recordingInstruments{}

		registry, err := NewRegistry(WithMetricsProvider(instruments.provider()))
		must.NoError(t, err)

		failure := platformerrors.New("connection refused")
		registry.Register(&mockChecker{name: "database"})
		registry.Register(&mockChecker{name: "cache", checkFn: func(context.Context) error { return failure }})

		result := registry.CheckAll(t.Context())
		test.EqOp(t, StatusDown, result.Status)

		transitions, downs := instruments.counts()
		test.EqOp(t, 2, transitions)
		test.Eq(t, []int64{1}, downs)
	})
}
