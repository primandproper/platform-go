package routingcfg

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v5/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"
	"github.com/primandproper/platform-go/v5/routing"
	"github.com/primandproper/platform-go/v5/routing/chi"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterRouter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, &Config{
			Provider: ProviderChi,
			Chi:      &chi.Config{ServiceName: t.Name()},
		})
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, metricsnoop.NewMetricsProvider())
		do.ProvideValue(i, testEncoder())

		RegisterRouter(i)

		backend, err := do.Invoke[routing.Backend](i)
		must.NoError(t, err)
		test.NotNil(t, backend)

		router, err := do.Invoke[*routing.Router](i)
		must.NoError(t, err)
		test.NotNil(t, router)
	})
}
