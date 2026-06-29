package circuitbreakingcfg

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v2/circuitbreaking/noop"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v2/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v2/observability/metrics/noop"

	circuit "github.com/rubyist/circuitbreaker"
	"github.com/shoenig/test"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		cfg := &Config{
			Name:                   t.Name(),
			ErrorRate:              0.99,
			MinimumSampleThreshold: 123,
		}

		err := cfg.ValidateWithContext(ctx)
		test.NoError(t, err)
	})

	T.Run("with missing name", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		cfg := &Config{
			Name:      "",
			ErrorRate: 0.99,
		}

		err := cfg.ValidateWithContext(ctx)
		test.Error(t, err)
	})

	T.Run("with error rate exceeding max", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		cfg := &Config{
			Name:      t.Name(),
			ErrorRate: 200,
		}

		err := cfg.ValidateWithContext(ctx)
		test.Error(t, err)
	})
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("with empty config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, "UNKNOWN", cfg.Name)
		test.EqOp(t, float64(100), cfg.ErrorRate)
		test.EqOp(t, uint64(1_000_000), cfg.MinimumSampleThreshold)
	})

	T.Run("does not override set values", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Name:                   "test",
			ErrorRate:              50.0,
			MinimumSampleThreshold: 500,
		}
		cfg.EnsureDefaults()

		test.EqOp(t, "test", cfg.Name)
		test.EqOp(t, 50.0, cfg.ErrorRate)
		test.EqOp(t, uint64(500), cfg.MinimumSampleThreshold)
	})
}

//nolint:paralleltest // race condition in the core circuit breaker library, I think?
func TestProvideCircuitBreakerFromConfig(T *testing.T) {
	T.Run("standard", func(t *testing.T) {
		cfg := &Config{}
		cfg.EnsureDefaults()

		ctx := t.Context()

		cb, err := ProvideCircuitBreakerFromConfig(ctx, cfg, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider())
		test.NotNil(t, cb)
		test.NoError(t, err)
	})

	T.Run("with metric attributes", func(t *testing.T) {
		cfg := &Config{}
		cfg.EnsureDefaults()

		ctx := t.Context()

		cb, err := cfg.ProvideCircuitBreaker(ctx, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider(),
			WithMetricAttributes(attribute.String("partition", "123")))
		test.NotNil(t, cb)
		test.NoError(t, err)
	})

	T.Run("with error providing first metric", func(t *testing.T) {
		cfg := &Config{}
		cfg.EnsureDefaults()

		ctx := t.Context()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, fmt.Sprintf("%s_circuit_breaker_tripped", cfg.Name), counterName)
				return &mockmetrics.Int64CounterMock{}, errors.New("arbitrary")
			},
		}

		cb, err := ProvideCircuitBreakerFromConfig(ctx, cfg, loggingnoop.NewLogger(), mp)
		test.Nil(t, cb)
		test.Error(t, err)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error providing second metric", func(t *testing.T) {
		cfg := &Config{}
		cfg.EnsureDefaults()

		ctx := t.Context()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case fmt.Sprintf("%s_circuit_breaker_tripped", cfg.Name):
					return &mockmetrics.Int64CounterMock{}, nil
				case fmt.Sprintf("%s_circuit_breaker_failed", cfg.Name):
					return &mockmetrics.Int64CounterMock{}, errors.New("arbitrary")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		cb, err := ProvideCircuitBreakerFromConfig(ctx, cfg, loggingnoop.NewLogger(), mp)
		test.Nil(t, cb)
		test.Error(t, err)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("with error providing third metric", func(t *testing.T) {
		cfg := &Config{}
		cfg.EnsureDefaults()

		ctx := t.Context()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case fmt.Sprintf("%s_circuit_breaker_tripped", cfg.Name),
					fmt.Sprintf("%s_circuit_breaker_failed", cfg.Name):
					return &mockmetrics.Int64CounterMock{}, nil
				case fmt.Sprintf("%s_circuit_breaker_reset", cfg.Name):
					return &mockmetrics.Int64CounterMock{}, errors.New("arbitrary")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		cb, err := ProvideCircuitBreakerFromConfig(ctx, cfg, loggingnoop.NewLogger(), mp)
		test.Nil(t, cb)
		test.Error(t, err)

		test.SliceLen(t, 3, mp.NewInt64CounterCalls())
	})
}

//nolint:paralleltest // race condition in the core circuit breaker library, I think?
func TestEnsureCircuitBreaker(T *testing.T) {
	T.Run("with nil breaker", func(t *testing.T) {
		actual := EnsureCircuitBreaker(nil)
		test.NotNil(t, actual)
	})

	T.Run("with non-nil breaker", func(t *testing.T) {
		input := noop.NewCircuitBreaker()
		actual := EnsureCircuitBreaker(input)
		test.Eq(t, input, actual)
	})
}

//nolint:paralleltest // race condition in the core circuit breaker library, I think?
func TestConfig_ProvideCircuitBreaker(T *testing.T) {
	T.Run("with nil config", func(t *testing.T) {
		ctx := t.Context()

		var cfg *Config
		cb, err := cfg.ProvideCircuitBreaker(ctx, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider())
		test.Nil(t, cb)
		test.Error(t, err)
	})

	T.Run("with invalid config", func(t *testing.T) {
		ctx := t.Context()

		cfg := &Config{
			Name:      "",
			ErrorRate: 200,
		}

		cb, err := cfg.ProvideCircuitBreaker(ctx, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider())
		test.NotNil(t, cb)
		test.NoError(t, err)
	})
}

//nolint:paralleltest // race condition in the core circuit breaker library, I think?
func TestBaseImplementation(T *testing.T) {
	T.Run("Failed", func(t *testing.T) {
		ctx := t.Context()

		cfg := &Config{
			Name:                   t.Name(),
			ErrorRate:              99,
			MinimumSampleThreshold: 1000,
		}

		cb, err := cfg.ProvideCircuitBreaker(ctx, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider())
		test.NotNil(t, cb)
		test.NoError(t, err)

		cb.Failed()
	})

	T.Run("Succeeded", func(t *testing.T) {
		ctx := t.Context()

		cfg := &Config{
			Name:                   t.Name(),
			ErrorRate:              99,
			MinimumSampleThreshold: 1000,
		}

		cb, err := cfg.ProvideCircuitBreaker(ctx, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider())
		test.NotNil(t, cb)
		test.NoError(t, err)

		cb.Succeeded()
	})

	T.Run("CanProceed", func(t *testing.T) {
		ctx := t.Context()

		cfg := &Config{
			Name:                   t.Name(),
			ErrorRate:              99,
			MinimumSampleThreshold: 1000,
		}

		cb, err := cfg.ProvideCircuitBreaker(ctx, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider())
		test.NotNil(t, cb)
		test.NoError(t, err)

		test.True(t, cb.CanProceed())
	})

	T.Run("CannotProceed", func(t *testing.T) {
		ctx := t.Context()

		cfg := &Config{
			Name:                   t.Name(),
			ErrorRate:              99,
			MinimumSampleThreshold: 1000,
		}

		cb, err := cfg.ProvideCircuitBreaker(ctx, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider())
		test.NotNil(t, cb)
		test.NoError(t, err)

		test.False(t, cb.CannotProceed())
	})
}

//nolint:paralleltest // race condition in the core circuit breaker library, I think?
func TestHandleCircuitBreakerEvents(T *testing.T) {
	T.Run("handles all event types and exits on channel close", func(t *testing.T) {
		ctx := t.Context()

		i64Counter := &mockmetrics.Int64CounterMock{
			AddFunc: func(_ context.Context, _ int64, _ ...metric.AddOption) {},
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case "failure", "reset", "broken":
					return i64Counter, nil
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		failure, err := mp.NewInt64Counter("failure")
		test.NoError(t, err)
		reset, err := mp.NewInt64Counter("reset")
		test.NoError(t, err)
		broken, err := mp.NewInt64Counter("broken")
		test.NoError(t, err)

		events := make(chan circuit.BreakerEvent, 4)
		events <- circuit.BreakerTripped
		events <- circuit.BreakerReset
		events <- circuit.BreakerFail
		events <- circuit.BreakerReady
		close(events)

		handleCircuitBreakerEvents(ctx, loggingnoop.NewLogger(), events, failure, reset, broken)

		test.SliceLen(t, 3, mp.NewInt64CounterCalls())
		test.SliceLen(t, 3, i64Counter.AddCalls())
	})
}

//nolint:paralleltest // race condition in the core circuit breaker library, I think?
func TestCircuitBreaker_Integration(T *testing.T) {
	T.Run("standard", func(t *testing.T) {
		t.SkipNow() // cannot run this with the race detector on

		ctx := t.Context()

		cfg := &Config{
			Name:                   t.Name(),
			ErrorRate:              1,
			MinimumSampleThreshold: 1,
		}

		cb, err := ProvideCircuitBreakerFromConfig(ctx, cfg, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider())
		test.NotNil(t, cb)
		test.NoError(t, err)

		test.True(t, cb.CanProceed())
		cb.Failed()
		test.True(t, cb.CannotProceed())
		cb.Succeeded()
		deadline := time.Now().Add(5 * time.Second)
		var proceeded bool
		for time.Now().Before(deadline) {
			if cb.CanProceed() {
				proceeded = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		test.True(t, proceeded)
	})
}
