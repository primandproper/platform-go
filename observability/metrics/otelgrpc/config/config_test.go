package config

import (
	"testing"
	"time"

	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/metrics/otelgrpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			CollectorEndpoint: t.Name(),
			ServiceName:       t.Name(),
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("missing collector endpoint", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			ServiceName: t.Name(),
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("missing service name", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			CollectorEndpoint: t.Name(),
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestProvideMetricsProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			ServiceName:       t.Name(),
			CollectorEndpoint: "localhost:4317",
			Otel: &otelgrpc.Config{
				CollectorEndpoint:  "localhost:4317",
				CollectionInterval: 30 * time.Second,
				Insecure:           true,
			},
		}

		provider, err := ProvideMetricsProvider(t.Context(), loggingnoop.NewLogger(), cfg)
		must.NoError(t, err)
		test.NotNil(t, provider)
	})

	T.Run("with nil otel config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			ServiceName:       t.Name(),
			CollectorEndpoint: "localhost:4317",
		}

		provider, err := ProvideMetricsProvider(t.Context(), loggingnoop.NewLogger(), cfg)
		test.Nil(t, provider)
		test.Error(t, err)
	})
}
