package noop

import (
	"github.com/primandproper/platform-go/v2/circuitbreaking"
	cbnoop "github.com/primandproper/platform-go/v2/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v2/circuitbreaking/partitioned"
)

var _ partitioned.KeyedCircuitBreaker = (*keyedCircuitBreaker)(nil)

// keyedCircuitBreaker is a no-op implementation that always allows operations to proceed.
type keyedCircuitBreaker struct {
	breaker circuitbreaking.CircuitBreaker
}

// NewKeyedCircuitBreaker returns a KeyedCircuitBreaker that always allows operations to proceed.
func NewKeyedCircuitBreaker() partitioned.KeyedCircuitBreaker {
	return &keyedCircuitBreaker{
		breaker: cbnoop.NewCircuitBreaker(),
	}
}

func (n *keyedCircuitBreaker) For(string) circuitbreaking.CircuitBreaker {
	return n.breaker
}
