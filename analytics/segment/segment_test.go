package segment

import (
	"errors"
	"testing"

	circuitbreakingmock "github.com/primandproper/platform-go/v13/circuitbreaking/mock"
	cbnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/metrics/metricstest"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func TestBreakerCallback(T *testing.T) {
	T.Parallel()

	T.Run("delivery success records breaker success", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{SucceededFunc: func() {}}
		callback := &breakerCallback{
			circuitBreaker: cb,
			errorCounter:   metricstest.Int64Counter(t, "x"),
			logger:         loggingnoop.NewLogger(),
		}

		callback.Success(nil)
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("delivery failure trips the breaker", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{FailedFunc: func() {}}
		callback := &breakerCallback{
			circuitBreaker: cb,
			errorCounter:   metricstest.Int64Counter(t, "x"),
			logger:         loggingnoop.NewLogger(),
		}

		callback.Failure(nil, errors.New("delivery boom"))
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

// newRecordingEventReporter builds an EventReporter with a RecordingObserver
// swapped in, so a test can both drive a method and assert which fields it observed.
func newRecordingEventReporter(t *testing.T) (*EventReporter, *observability.RecordingObserver) {
	t.Helper()

	c, err := NewEventReporter(t.Name(), cbnoop.NewCircuitBreaker())
	must.NoError(t, err)
	must.NotNil(t, c)

	obs := observability.NewRecordingObserver()
	c.o11y = obs

	return c, obs
}

func TestNewEventReporter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		collector, err := NewEventReporter(t.Name(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)
	})

	T.Run("with empty API key", func(t *testing.T) {
		t.Parallel()

		collector, err := NewEventReporter("", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, collector)
	})

	T.Run("with error creating event counter", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, name+"_events", counterName)
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		collector, err := NewEventReporter(t.Name(), cbnoop.NewCircuitBreaker(), WithMetricsProvider(mp))
		must.Error(t, err)
		must.Nil(t, collector)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating error counter", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case name + "_events":
					return metricstest.Int64Counter(t, "x"), nil
				case name + "_errors":
					return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		collector, err := NewEventReporter(t.Name(), cbnoop.NewCircuitBreaker(), WithMetricsProvider(mp))
		must.Error(t, err)
		must.Nil(t, collector)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})
}

func TestSegmentEventReporter_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		collector, err := NewEventReporter(t.Name(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)

		_ = collector.Close(t.Context())
	})
}

func TestSegmentEventReporter_AddUser(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleUserID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		collector, obs := newRecordingEventReporter(t)

		must.NoError(t, collector.AddUser(ctx, exampleUserID, properties))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: exampleUserID,
		})
	})
}

func TestSegmentEventReporter_EventOccurred(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleUserID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		collector, obs := newRecordingEventReporter(t)

		must.NoError(t, collector.EventOccurred(ctx, t.Name(), exampleUserID, properties))

		obs.ObservedOperationWithData(t, map[string]any{
			"event":        t.Name(),
			keys.UserIDKey: exampleUserID,
		})
	})
}

func TestSegmentEventReporter_EventOccurredAnonymous(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleAnonymousID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		collector, obs := newRecordingEventReporter(t)

		must.NoError(t, collector.EventOccurredAnonymous(ctx, t.Name(), exampleAnonymousID, properties))

		obs.ObservedOperationWithData(t, map[string]any{
			"event":        t.Name(),
			keys.UserIDKey: exampleAnonymousID,
		})
	})
}
