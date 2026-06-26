package segment

import (
	"errors"
	"testing"

	cbnoop "github.com/primandproper/platform-go/circuitbreaking/noop"
	"github.com/primandproper/platform-go/identifiers"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func TestNewSegmentEventReporter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		collector, err := NewSegmentEventReporter(logger, tracingnoop.NewTracerProvider(), nil, t.Name(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)
	})

	T.Run("with empty API key", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		collector, err := NewSegmentEventReporter(logger, tracingnoop.NewTracerProvider(), nil, "", cbnoop.NewCircuitBreaker())
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

		collector, err := NewSegmentEventReporter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, t.Name(), cbnoop.NewCircuitBreaker())
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

		collector, err := NewSegmentEventReporter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, t.Name(), cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		must.Nil(t, collector)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})
}

func TestSegmentEventReporter_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		collector, err := NewSegmentEventReporter(logger, tracingnoop.NewTracerProvider(), nil, t.Name(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)

		collector.Close()
	})
}

func TestSegmentEventReporter_AddUser(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		exampleUserID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		collector, err := NewSegmentEventReporter(logger, tracingnoop.NewTracerProvider(), nil, t.Name(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)

		must.NoError(t, collector.AddUser(ctx, exampleUserID, properties))
	})
}

func TestSegmentEventReporter_EventOccurred(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		exampleUserID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		collector, err := NewSegmentEventReporter(logger, tracingnoop.NewTracerProvider(), nil, t.Name(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)

		must.NoError(t, collector.EventOccurred(ctx, t.Name(), exampleUserID, properties))
	})
}

func TestSegmentEventReporter_EventOccurredAnonymous(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		exampleAnonymousID := identifiers.New()
		properties := map[string]any{
			"test.name": t.Name(),
		}

		collector, err := NewSegmentEventReporter(logger, tracingnoop.NewTracerProvider(), nil, t.Name(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, collector)

		must.NoError(t, collector.EventOccurredAnonymous(ctx, t.Name(), exampleAnonymousID, properties))
	})
}
