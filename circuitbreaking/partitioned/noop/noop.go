// Package noop is the partitioned.KeyedCircuitBreaker that never opens for any
// key. For hands back one shared circuitbreaking/noop breaker rather than a
// breaker per key, which is safe only because that breaker holds no state:
// there is nothing per-key to keep, so there is no reason to allocate per key
// or to bound the map that would hold them.
//
// Selecting it means no partition is ever isolated from any other. The reason
// to key a breaker at all is that one bad tenant, shard, or upstream trips only
// its own circuit and leaves the rest alone; here nothing trips, so a failing
// partition keeps taking traffic beside the healthy ones and the blast radius
// is once again the whole service.
package noop

import (
	"github.com/primandproper/platform-go/v13/circuitbreaking"
	cbnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v13/circuitbreaking/partitioned"
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
