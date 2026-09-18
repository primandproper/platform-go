package push_test

import (
	"context"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/notifications/push"

	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	metricsmock "github.com/primandproper/primitives-go/v2/observability/metrics/mock"
	metricsnoop "github.com/primandproper/primitives-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// recordingCounter is one instrument's worth of what the fan-out added to it.
type recordingCounter struct {
	added []int64
	mu    sync.Mutex
}

var _ metrics.Int64Counter = (*recordingCounter)(nil)

func (c *recordingCounter) Add(_ context.Context, value int64, _ ...metric.AddOption) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.added = append(c.added, value)
}

func (c *recordingCounter) total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	var sum int64
	for _, v := range c.added {
		sum += v
	}

	return sum
}

// countingProvider answers with a recording counter for the two instruments
// these tests read and with the noop provider's for everything else.
func countingProvider(requests, errs *recordingCounter) metrics.Provider {
	base := metricsnoop.NewMetricsProvider()

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, o ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			switch name {
			case "notifications_push_fanout_requests":
				return requests, nil
			case "notifications_push_fanout_errors":
				return errs, nil
			default:
				return base.NewInt64Counter(name, o...)
			}
		},
		NewFloat64HistogramFunc: base.NewFloat64Histogram,
	}
}

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("reports an instrument it could not build", func(t *testing.T) {
		t.Parallel()

		provider := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return nil, errInstrumentUnavailable
			},
		}

		fanout, err := push.NewFanout(resolving(nil, nil), newStubSender(nil),
			push.WithMetricsProvider(provider))
		test.Nil(t, fanout)
		test.ErrorIs(t, err, errInstrumentUnavailable)
	})

	T.Run("counts a send and the send that failed", func(t *testing.T) {
		t.Parallel()

		// Errors are a subset of requests rather than a series beside them,
		// which is what makes their ratio the push error rate.
		requests, errs := &recordingCounter{}, &recordingCounter{}

		fanout := newFanout(t, resolving([]*notifications.Device{
			device(firstPrincipal, notifications.PlatformIOS, "token-a"),
			device(firstPrincipal, notifications.PlatformAndroid, "token-b"),
		}, nil), newStubSender(map[string]error{"token-b": errProviderUnreachable}),
			push.WithMetricsProvider(countingProvider(requests, errs)))

		_, err := fanout.Push(t.Context(), reader, testScope, []string{firstPrincipal}, testMessage)
		test.ErrorIs(t, err, errProviderUnreachable)

		// Two handsets, two attempts — the unit is the send rather than the
		// announcement, which covers one handset or three hundred.
		test.EqOp(t, int64(2), requests.total())
		test.EqOp(t, int64(1), errs.total())
	})

	T.Run("takes the three pillars at once", func(t *testing.T) {
		t.Parallel()

		pillars := &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		}

		fanout, err := push.NewFanout(resolving(nil, nil), newStubSender(nil),
			push.WithPillars(pillars))
		must.NoError(t, err)
		test.NotNil(t, fanout)
	})

	T.Run("applies options in order", func(t *testing.T) {
		t.Parallel()

		// WithPillars followed by WithMetricsProvider(nil) leaves this one
		// component unmetered, which is the whole of what "options apply in
		// order" buys.
		requests, errs := &recordingCounter{}, &recordingCounter{}

		pillars := &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: countingProvider(requests, errs),
		}

		fanout := newFanout(t, resolving([]*notifications.Device{
			device(firstPrincipal, notifications.PlatformIOS, "token-a"),
		}, nil), newStubSender(nil),
			push.WithPillars(pillars), push.WithMetricsProvider(nil))

		_, err := fanout.Push(t.Context(), reader, testScope, []string{firstPrincipal}, testMessage)
		must.NoError(t, err)

		test.EqOp(t, int64(0), requests.total())
	})

	T.Run("takes a logger and a tracer provider", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = loggingnoop.NewLogger()

		fanout, err := push.NewFanout(resolving(nil, nil), newStubSender(nil),
			push.WithLogger(logger),
			push.WithTracerProvider(tracingnoop.NewTracerProvider()))
		must.NoError(t, err)
		test.NotNil(t, fanout)
	})
}
