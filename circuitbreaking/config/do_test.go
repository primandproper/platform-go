package circuitbreakingcfg

import (
	"testing"

	"github.com/primandproper/platform-go/v2/circuitbreaking"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v2/observability/metrics/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

//nolint:paralleltest // race condition in the core circuit breaker library, I think?
func TestRegisterCircuitBreaker(T *testing.T) {
	T.Run("standard", func(t *testing.T) {
		cfg := &Config{}
		cfg.EnsureDefaults()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, metricsnoop.NewMetricsProvider())
		do.ProvideValue(i, cfg)

		RegisterCircuitBreaker(i)

		cb, err := do.Invoke[circuitbreaking.CircuitBreaker](i)
		must.NoError(t, err)
		test.NotNil(t, cb)
	})
}
