package posthog

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

// newRecordingReporter builds an EventReporter with a RecordingObserver swapped
// in, so a test can both drive the c and assert which fields it observed.
func newRecordingReporter(t *testing.T, apiKey string) (*EventReporter, *observability.RecordingObserver) {
	t.Helper()

	c, err := NewEventReporter(apiKey, cbnoop.NewCircuitBreaker())
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

		cfg := &Config{APIKey: t.Name()}

		collector, err := NewEventReporter(cfg.APIKey, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)
	})

	T.Run("with empty API key", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		collector, err := NewEventReporter(cfg.APIKey, cbnoop.NewCircuitBreaker())
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

func TestBreakerCallback(T *testing.T) {
	T.Parallel()

	T.Run("Success drives the circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{SucceededFunc: func() {}}
		cbk := &breakerCallback{
			circuitBreaker: cb,
			errorCounter:   metricstest.Int64Counter(t, name+"_errors"),
			logger:         loggingnoop.NewLogger(),
		}

		cbk.Success(nil)

		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("Success tolerates a nil circuit breaker", func(t *testing.T) {
		t.Parallel()

		cbk := &breakerCallback{
			errorCounter: metricstest.Int64Counter(t, name+"_errors"),
			logger:       loggingnoop.NewLogger(),
		}

		test.NotPanic(t, func() { cbk.Success(nil) })
	})

	T.Run("Failure records the error and trips the circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{FailedFunc: func() {}}
		cbk := &breakerCallback{
			circuitBreaker: cb,
			errorCounter:   metricstest.Int64Counter(t, name+"_errors"),
			logger:         loggingnoop.NewLogger(),
		}

		cbk.Failure(nil, errors.New("delivery failed"))

		test.SliceLen(t, 1, cb.FailedCalls())
	})

	T.Run("Failure tolerates a nil circuit breaker", func(t *testing.T) {
		t.Parallel()

		cbk := &breakerCallback{
			errorCounter: metricstest.Int64Counter(t, name+"_errors"),
			logger:       loggingnoop.NewLogger(),
		}

		test.NotPanic(t, func() { cbk.Failure(nil, errors.New("delivery failed")) })
	})
}

func TestPostHogEventReporter_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{APIKey: t.Name()}

		collector, err := NewEventReporter(cfg.APIKey, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)

		_ = collector.Close(t.Context())
	})
}

func TestPostHogEventReporter_AddUser(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleUserID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		c, obs := newRecordingReporter(t, t.Name())

		must.NoError(t, c.AddUser(ctx, exampleUserID, properties))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: exampleUserID,
		})
	})

	T.Run("with error enqueueing", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		c, obs := newRecordingReporter(t, t.Name())

		// An empty distinct ID fails the client's Validate, exercising the error path.
		must.Error(t, c.AddUser(ctx, "", properties))

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: "",
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestPostHogEventReporter_EventOccurred(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleUserID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		c, obs := newRecordingReporter(t, t.Name())

		must.NoError(t, c.EventOccurred(ctx, t.Name(), exampleUserID, properties))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: exampleUserID,
			"event":        t.Name(),
		})
	})

	T.Run("with error enqueueing", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleUserID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		c, obs := newRecordingReporter(t, t.Name())

		// An empty event fails the client's Validate, exercising the error path.
		must.Error(t, c.EventOccurred(ctx, "", exampleUserID, properties))

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: exampleUserID,
			"event":        "",
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestPostHogEventReporter_EventOccurredAnonymous(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleAnonymousID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		c, obs := newRecordingReporter(t, t.Name())

		must.NoError(t, c.EventOccurredAnonymous(ctx, t.Name(), exampleAnonymousID, properties))

		obs.ObservedOperationWithData(t, map[string]any{
			"anonymous_id": exampleAnonymousID,
			"event":        t.Name(),
		})
	})

	T.Run("with error enqueueing", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleAnonymousID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		c, obs := newRecordingReporter(t, t.Name())

		// An empty event fails the client's Validate, exercising the error path.
		must.Error(t, c.EventOccurredAnonymous(ctx, "", exampleAnonymousID, properties))

		op := obs.ObservedOperationWithData(t, map[string]any{
			"anonymous_id": exampleAnonymousID,
			"event":        "",
		})
		must.SliceLen(t, 1, op.Errors)
	})
}
