package config

import (
	"testing"
	"time"

	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/metrics/otelgrpc"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterMetricsProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, &Config{
			ServiceName:       t.Name(),
			CollectorEndpoint: "localhost:4317",
			Otel: &otelgrpc.Config{
				CollectorEndpoint:  "localhost:4317",
				CollectionInterval: 30 * time.Second,
				Insecure:           true,
			},
		})

		RegisterMetricsProvider(i)

		provider, err := do.Invoke[metrics.Provider](i)
		must.NoError(t, err)
		test.NotNil(t, provider)
	})
}
