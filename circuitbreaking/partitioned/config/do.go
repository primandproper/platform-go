package partitionedcfg

import (
	"context"

	"github.com/primandproper/platform/circuitbreaking/partitioned"
	"github.com/primandproper/platform/observability/logging"
	"github.com/primandproper/platform/observability/metrics"

	"github.com/samber/do/v2"
)

// RegisterKeyedCircuitBreaker registers a KeyedCircuitBreaker with the injector.
func RegisterKeyedCircuitBreaker(i do.Injector) {
	do.Provide[partitioned.KeyedCircuitBreaker](i, func(i do.Injector) (partitioned.KeyedCircuitBreaker, error) {
		return ProvideKeyedCircuitBreakerFromConfig(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})
}
