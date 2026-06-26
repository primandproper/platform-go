package routingcfg

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"
	"github.com/primandproper/platform-go/routing/chi"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderChi,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with invalid provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "bogus",
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestProvideRouter(T *testing.T) {
	T.Parallel()

	T.Run("with chi provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderChi,
			Chi:      &chi.Config{ServiceName: t.Name()},
		}

		router, err := ProvideRouter(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		test.NotNil(t, router)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "bogus",
		}

		router, err := ProvideRouter(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		test.Nil(t, router)
		test.Error(t, err)
	})
}

func TestConfig_ProvideRouter(T *testing.T) {
	T.Parallel()

	T.Run("with chi provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderChi,
			Chi:      &chi.Config{ServiceName: t.Name()},
		}

		router, err := cfg.ProvideRouter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		test.NotNil(t, router)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "bogus",
		}

		router, err := cfg.ProvideRouter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		test.Nil(t, router)
		test.Error(t, err)
	})
}

func TestProvideRouteParamManager(T *testing.T) {
	T.Parallel()

	T.Run("with chi provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderChi,
		}

		manager, err := ProvideRouteParamManager(cfg)
		must.NoError(t, err)
		test.NotNil(t, manager)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "bogus",
		}

		manager, err := ProvideRouteParamManager(cfg)
		test.Nil(t, manager)
		test.Error(t, err)
	})
}

func TestProvideRouterViaConfig(T *testing.T) {
	T.Parallel()

	T.Run("with chi provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderChi,
			Chi:      &chi.Config{ServiceName: t.Name()},
		}

		router, err := ProvideRouterViaConfig(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		test.NotNil(t, router)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "bogus",
		}

		router, err := ProvideRouterViaConfig(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		test.Nil(t, router)
		test.Error(t, err)
	})
}
