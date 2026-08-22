package circuitbreakingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterCircuitBreaker registers a CircuitBreaker with the injector.
func RegisterCircuitBreaker(i do.Injector) {
	do.Provide(i, func(i do.Injector) (circuitbreaking.CircuitBreaker, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewCircuitBreaker(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}
