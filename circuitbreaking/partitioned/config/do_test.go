package partitionedcfg

import (
	"testing"

	circuitbreakingcfg "github.com/primandproper/platform-go/v3/circuitbreaking/config"
	"github.com/primandproper/platform-go/v3/circuitbreaking/partitioned"
	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v3/observability/metrics/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

//nolint:paralleltest // race condition in the core circuit breaker library, I think?
func TestRegisterKeyedCircuitBreaker(T *testing.T) {
	T.Run("standard", func(t *testing.T) {
		cfg := &Config{
			Base: circuitbreakingcfg.Config{Name: t.Name()},
			Keys: []string{"123"},
		}
		cfg.EnsureDefaults()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, metricsnoop.NewMetricsProvider())
		do.ProvideValue(i, cfg)

		RegisterKeyedCircuitBreaker(i)

		cb, err := do.Invoke[partitioned.KeyedCircuitBreaker](i)
		must.NoError(t, err)
		test.NotNil(t, cb)
	})
}
