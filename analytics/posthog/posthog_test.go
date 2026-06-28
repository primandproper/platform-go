package posthog

import (
	"errors"
	"testing"

	cbnoop "github.com/primandproper/platform-go/circuitbreaking/noop"
	"github.com/primandproper/platform-go/identifiers"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// newRecordingReporter builds an EventReporter with a RecordingObserver swapped
// in, so a test can both drive the reporter and assert which fields it observed.
func newRecordingReporter(t *testing.T, apiKey string) (*EventReporter, *observability.RecordingObserver) {
	t.Helper()

	reporter, err := NewPostHogEventReporter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, apiKey, cbnoop.NewCircuitBreaker())
	must.NoError(t, err)
	must.NotNil(t, reporter)

	c, ok := reporter.(*EventReporter)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	c.o11y = obs

	return c, obs
}

func TestNewPostHogEventReporter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{APIKey: t.Name()}

		collector, err := NewPostHogEventReporter(logger, tracingnoop.NewTracerProvider(), nil, cfg.APIKey, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)
	})

	T.Run("with empty API key", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{}

		collector, err := NewPostHogEventReporter(logger, tracingnoop.NewTracerProvider(), nil, cfg.APIKey, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, collector)
	})

	T.Run("with error creating event counter", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, name+"_events", counterName)
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		collector, err := NewPostHogEventReporter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, t.Name(), cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, collector)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating error counter", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case name + "_events":
					return metrics.Int64CounterForTest(t, "x"), nil
				case name + "_errors":
					return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		collector, err := NewPostHogEventReporter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, t.Name(), cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, collector)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})
}

func TestPostHogEventReporter_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{APIKey: t.Name()}

		collector, err := NewPostHogEventReporter(logger, tracingnoop.NewTracerProvider(), nil, cfg.APIKey, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)

		collector.Close()
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
