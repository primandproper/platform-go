package routingcfg

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v4/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"
	"github.com/primandproper/platform-go/v4/routing/chi"

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

func TestNewRouter(T *testing.T) {
	T.Parallel()

	T.Run("with chi provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderChi,
			Chi:      &chi.Config{ServiceName: t.Name()},
		}

		router, err := NewRouter(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		test.NotNil(t, router)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "bogus",
		}

		router, err := NewRouter(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		test.Nil(t, router)
		test.Error(t, err)
	})
}

func TestConfig_NewRouter(T *testing.T) {
	T.Parallel()

	T.Run("with chi provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderChi,
			Chi:      &chi.Config{ServiceName: t.Name()},
		}

		router, err := cfg.NewRouter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		test.NotNil(t, router)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "bogus",
		}

		router, err := cfg.NewRouter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		test.Nil(t, router)
		test.Error(t, err)
	})
}

func TestNewRouteParamManager(T *testing.T) {
	T.Parallel()

	T.Run("with chi provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderChi,
		}

		manager, err := NewRouteParamManager(cfg)
		must.NoError(t, err)
		test.NotNil(t, manager)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "bogus",
		}

		manager, err := NewRouteParamManager(cfg)
		test.Nil(t, manager)
		test.Error(t, err)
	})
}

func TestNewRouterViaConfig(T *testing.T) {
	T.Parallel()

	T.Run("with chi provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderChi,
			Chi:      &chi.Config{ServiceName: t.Name()},
		}

		router, err := NewRouter(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		test.NotNil(t, router)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "bogus",
		}

		router, err := NewRouter(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider())
		test.Nil(t, router)
		test.Error(t, err)
	})
}
