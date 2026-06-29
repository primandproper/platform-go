package partitionedcfg

import (
	"errors"
	"fmt"
	"testing"

	circuitbreakingcfg "github.com/primandproper/platform-go/v2/circuitbreaking/config"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v2/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v2/observability/metrics/noop"

	"github.com/shoenig/test"
	"go.opentelemetry.io/otel/metric"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		cfg := &Config{
			Base: circuitbreakingcfg.Config{
				Name:                   t.Name(),
				ErrorRate:              0.99,
				MinimumSampleThreshold: 123,
			},
			Keys: []string{"123", "456"},
		}

		err := cfg.ValidateWithContext(ctx)
		test.NoError(t, err)
	})

	T.Run("with invalid base config", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		cfg := &Config{
			Base: circuitbreakingcfg.Config{
				Name:      "",
				ErrorRate: 200,
			},
		}

		err := cfg.ValidateWithContext(ctx)
		test.Error(t, err)
	})

	T.Run("with empty key", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		cfg := &Config{
			Base: circuitbreakingcfg.Config{
				Name:      t.Name(),
				ErrorRate: 0.99,
			},
			Keys: []string{"123", ""},
		}

		err := cfg.ValidateWithContext(ctx)
		test.Error(t, err)
	})
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("with empty config delegates to base", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, "UNKNOWN", cfg.Base.Name)
		test.EqOp(t, float64(100), cfg.Base.ErrorRate)
		test.EqOp(t, uint64(1_000_000), cfg.Base.MinimumSampleThreshold)
	})
}

//nolint:paralleltest // race condition in the core circuit breaker library, I think?
func TestProvideKeyedCircuitBreakerFromConfig(T *testing.T) {
	T.Run("standard", func(t *testing.T) {
		cfg := &Config{
			Base: circuitbreakingcfg.Config{Name: t.Name()},
			Keys: []string{"123"},
		}
		cfg.EnsureDefaults()

		ctx := t.Context()

		cb, err := ProvideKeyedCircuitBreakerFromConfig(ctx, cfg, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider())
		test.NotNil(t, cb)
		test.NoError(t, err)

		// a registered key gets its own breaker; unregistered keys share the global one.
		test.True(t, cb.For("123") != cb.For("456"))
		test.True(t, cb.For("456") == cb.For("789"))
	})

	T.Run("with error building the global breaker", func(t *testing.T) {
		cfg := &Config{
			Base: circuitbreakingcfg.Config{Name: t.Name()},
			Keys: []string{"123"},
		}
		cfg.EnsureDefaults()

		ctx := t.Context()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, fmt.Sprintf("%s_circuit_breaker_tripped", cfg.Base.Name), counterName)
				return &mockmetrics.Int64CounterMock{}, errors.New("arbitrary")
			},
		}

		cb, err := ProvideKeyedCircuitBreakerFromConfig(ctx, cfg, loggingnoop.NewLogger(), mp)
		test.Nil(t, cb)
		test.Error(t, err)
	})

	T.Run("with error building a keyed breaker", func(t *testing.T) {
		cfg := &Config{
			Base: circuitbreakingcfg.Config{Name: t.Name()},
			Keys: []string{"123"},
		}
		cfg.EnsureDefaults()

		ctx := t.Context()

		// the global breaker creates 3 counters successfully; fail the next one so the
		// per-key breaker build errors.
		var calls int
		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				calls++
				if calls > 3 {
					return &mockmetrics.Int64CounterMock{}, errors.New("arbitrary")
				}

				return &mockmetrics.Int64CounterMock{}, nil
			},
		}

		cb, err := ProvideKeyedCircuitBreakerFromConfig(ctx, cfg, loggingnoop.NewLogger(), mp)
		test.Nil(t, cb)
		test.Error(t, err)
	})
}

//nolint:paralleltest // race condition in the core circuit breaker library, I think?
func TestConfig_ProvideKeyedCircuitBreaker(T *testing.T) {
	T.Run("with nil config", func(t *testing.T) {
		ctx := t.Context()

		var cfg *Config
		cb, err := cfg.ProvideKeyedCircuitBreaker(ctx, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider())
		test.Nil(t, cb)
		test.Error(t, err)
	})

	T.Run("with invalid config", func(t *testing.T) {
		ctx := t.Context()

		cfg := &Config{
			Base: circuitbreakingcfg.Config{
				Name:      "",
				ErrorRate: 200,
			},
		}

		cb, err := cfg.ProvideKeyedCircuitBreaker(ctx, loggingnoop.NewLogger(), metricsnoop.NewMetricsProvider())
		test.NotNil(t, cb)
		test.NoError(t, err)
	})
}
