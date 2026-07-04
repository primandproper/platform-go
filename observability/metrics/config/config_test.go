package metricscfg

import (
	"testing"
	"time"

	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	"github.com/primandproper/platform-go/v3/observability/metrics/otelgrpc"

	"github.com/shoenig/test"
)

func TestConfig_ProvideMetricsProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		metricsProvider, err := cfg.ProvideMetricsProvider(t.Context(), loggingnoop.NewLogger())

		test.NoError(t, err)
		test.NotNil(t, metricsProvider)
	})

	T.Run("enabled with otel provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:     true,
			Provider:    ProviderOtel,
			ServiceName: t.Name(),
			Otel: &otelgrpc.Config{
				CollectorEndpoint:  "localhost:4317",
				CollectionInterval: 30 * time.Second,
				Insecure:           true,
			},
		}

		metricsProvider, err := cfg.ProvideMetricsProvider(t.Context(), loggingnoop.NewLogger())

		test.NoError(t, err)
		test.NotNil(t, metricsProvider)
	})

	T.Run("enabled with unknown provider falls back to noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  true,
			Provider: "unknown",
		}

		metricsProvider, err := cfg.ProvideMetricsProvider(t.Context(), loggingnoop.NewLogger())

		test.NoError(t, err)
		test.NotNil(t, metricsProvider)
	})

	T.Run("not enabled returns noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled: false,
		}

		metricsProvider, err := cfg.ProvideMetricsProvider(t.Context(), loggingnoop.NewLogger())

		test.NoError(t, err)
		test.NotNil(t, metricsProvider)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  true,
			Provider: ProviderOtel,
			Otel: &otelgrpc.Config{
				CollectorEndpoint:  t.Name(),
				CollectionInterval: 1,
			},
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("disabled is valid", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled: false,
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("enabled with invalid provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  true,
			Provider: "bogus",
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("enabled with otel provider but nil otel config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  true,
			Provider: ProviderOtel,
			Otel:     nil,
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestProvideMetricsProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		metricsProvider, err := ProvideMetricsProvider(t.Context(), loggingnoop.NewLogger(), cfg)

		test.NoError(t, err)
		test.NotNil(t, metricsProvider)
	})
}
