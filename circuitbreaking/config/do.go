package circuitbreakingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v3/circuitbreaking"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/metrics"

	"github.com/samber/do/v2"
)

// RegisterCircuitBreaker registers a CircuitBreaker with the injector.
func RegisterCircuitBreaker(i do.Injector) {
	do.Provide[circuitbreaking.CircuitBreaker](i, func(i do.Injector) (circuitbreaking.CircuitBreaker, error) {
		return ProvideCircuitBreakerFromConfig(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})
}
