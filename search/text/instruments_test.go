package textsearch

import (
	"context"
	"sync"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

var errInstrument = platformerrors.New("instrument unavailable")

func TestNewInstruments(T *testing.T) {
	T.Parallel()

	T.Run("a nil provider records nothing and builds anyway", func(t *testing.T) {
		t.Parallel()

		instruments, err := NewInstruments("backend", "index", nil)
		must.NoError(t, err)
		must.NotNil(t, instruments)

		instruments.Record(t.Context(), OperationSearch, time.Now(), nil)
	})

	for _, name := range []string{"backend_operations", "backend_errors", "backend_latency_ms"} {
		T.Run("refuses to build without "+name, func(t *testing.T) {
			t.Parallel()

			noop := metrics.EnsureMetricsProvider(nil)

			provider := &metricsmock.ProviderMock{
				NewInt64CounterFunc: func(n string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
					if n == name {
						return nil, errInstrument
					}

					return noop.NewInt64Counter(n, opts...)
				},
				NewFloat64HistogramFunc: func(n string, opts ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
					if n == name {
						return nil, errInstrument
					}

					return noop.NewFloat64Histogram(n, opts...)
				},
			}

			instruments, err := NewInstruments("backend", "index", provider)
			test.Nil(t, instruments)
			test.ErrorIs(t, err, errInstrument)
		})
	}
}

// counts is what a recording provider saw.
type counts struct {
	operations int
	failures   int
	latencies  int
	mu         sync.Mutex
}

func (c *counts) provider() *metricsmock.ProviderMock {
	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			target := &c.operations
			if name == "backend_errors" {
				target = &c.failures
			}

			return &metricsmock.Int64CounterMock{
				AddFunc: func(context.Context, int64, ...metric.AddOption) {
					c.mu.Lock()
					defer c.mu.Unlock()

					*target++
				},
			}, nil
		},
		NewFloat64HistogramFunc: func(string, ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
			return &recordingHistogram{owner: c}, nil
		},
	}
}

func (c *counts) read() (operations, failures, latencies int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.operations, c.failures, c.latencies
}

type recordingHistogram struct {
	owner *counts
}

func (h *recordingHistogram) Record(context.Context, float64, ...metric.RecordOption) {
	h.owner.mu.Lock()
	defer h.owner.mu.Unlock()

	h.owner.latencies++
}

func TestInstruments_Record(T *testing.T) {
	T.Parallel()

	T.Run("counts every operation and only the failed ones twice", func(t *testing.T) {
		t.Parallel()

		seen := &counts{}

		instruments, err := NewInstruments("backend", "index", seen.provider())
		must.NoError(t, err)

		instruments.Record(t.Context(), OperationIndex, time.Now(), nil)
		instruments.Record(t.Context(), OperationSearch, time.Now(), nil)
		instruments.Record(t.Context(), OperationSearch, time.Now(), platformerrors.New("boom"))

		// The failure counter is a numerator over the same population the
		// operation counter counts, so the ratio is the error rate directly.
		operations, failures, latencies := seen.read()
		test.EqOp(t, 3, operations)
		test.EqOp(t, 1, failures)
		test.EqOp(t, 3, latencies)
	})
}
