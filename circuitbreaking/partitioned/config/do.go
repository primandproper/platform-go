package partitionedcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/circuitbreaking/partitioned"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterKeyedCircuitBreaker registers a KeyedCircuitBreaker with the injector.
func RegisterKeyedCircuitBreaker(i do.Injector) {
	do.Provide(i, func(i do.Injector) (partitioned.KeyedCircuitBreaker, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewKeyedCircuitBreaker(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}
