package rudderstack

import (
	"errors"
	"testing"

	cbnoop "github.com/primandproper/platform-go/v2/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v2/identifiers"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v2/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// newRecordingEventReporter builds an EventReporter with a RecordingObserver
// swapped in, so a test can both drive its methods and assert which fields it
// observed.
func newRecordingEventReporter(t *testing.T, cfg *Config) (*EventReporter, *observability.RecordingObserver) {
	t.Helper()

	reporter, err := NewRudderstackEventReporter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cfg, cbnoop.NewCircuitBreaker())
	must.NoError(t, err)
	must.NotNil(t, reporter)

	c, ok := reporter.(*EventReporter)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	c.o11y = obs

	return c, obs
}

func TestNewRudderstackEventReporter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{
			APIKey:       t.Name(),
			DataPlaneURL: t.Name(),
		}

		collector, err := NewRudderstackEventReporter(logger, tracingnoop.NewTracerProvider(), nil, cfg, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		collector, err := NewRudderstackEventReporter(logger, tracingnoop.NewTracerProvider(), nil, nil, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, collector)
	})

	T.Run("with empty API key", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{
			APIKey:       "",
			DataPlaneURL: t.Name(),
		}

		collector, err := NewRudderstackEventReporter(logger, tracingnoop.NewTracerProvider(), nil, cfg, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, collector)
	})

	T.Run("with empty DataPlane URL", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{
			APIKey:       t.Name(),
			DataPlaneURL: "",
		}

		collector, err := NewRudderstackEventReporter(logger, tracingnoop.NewTracerProvider(), nil, cfg, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, collector)
	})

	T.Run("with error creating event counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			APIKey:       t.Name(),
			DataPlaneURL: t.Name(),
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, name+"_events", counterName)
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		collector, err := NewRudderstackEventReporter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, cfg, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, collector)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating error counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			APIKey:       t.Name(),
			DataPlaneURL: t.Name(),
		}

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

		collector, err := NewRudderstackEventReporter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, cfg, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, collector)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})
}

func TestRudderstackEventReporter_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{
			APIKey:       t.Name(),
			DataPlaneURL: t.Name(),
		}

		collector, err := NewRudderstackEventReporter(logger, tracingnoop.NewTracerProvider(), nil, cfg, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)

		collector.Close()
	})
}

func TestRudderstackEventReporter_AddUser(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleUserID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		cfg := &Config{
			APIKey:       t.Name(),
			DataPlaneURL: t.Name(),
		}

		collector, obs := newRecordingEventReporter(t, cfg)

		must.NoError(t, collector.AddUser(ctx, exampleUserID, properties))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.UserIDKey: exampleUserID,
		})
	})
}

func TestRudderstackEventReporter_EventOccurred(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleUserID := identifiers.New()
		exampleEvent := t.Name()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		cfg := &Config{
			APIKey:       t.Name(),
			DataPlaneURL: t.Name(),
		}

		collector, obs := newRecordingEventReporter(t, cfg)

		must.NoError(t, collector.EventOccurred(ctx, exampleEvent, exampleUserID, properties))

		obs.ObservedOperationWithData(t, map[string]any{
			"event":        exampleEvent,
			keys.UserIDKey: exampleUserID,
		})
	})
}

func TestRudderstackEventReporter_EventOccurredAnonymous(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		exampleAnonymousID := identifiers.New()
		exampleEvent := t.Name()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		cfg := &Config{
			APIKey:       t.Name(),
			DataPlaneURL: t.Name(),
		}

		collector, obs := newRecordingEventReporter(t, cfg)

		must.NoError(t, collector.EventOccurredAnonymous(ctx, exampleEvent, exampleAnonymousID, properties))

		obs.ObservedOperationWithData(t, map[string]any{
			"event":        exampleEvent,
			keys.UserIDKey: exampleAnonymousID,
		})
	})
}
