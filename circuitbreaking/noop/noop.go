package noop

import (
	"github.com/primandproper/platform-go/v6/circuitbreaking"
)

var _ circuitbreaking.CircuitBreaker = (*circuitBreaker)(nil)

// circuitBreaker is a no-op implementation that always allows operations to proceed.
type circuitBreaker struct{}

// NewCircuitBreaker returns a CircuitBreaker that always allows operations to proceed.
func NewCircuitBreaker() circuitbreaking.CircuitBreaker {
	return &circuitBreaker{}
}

func (n *circuitBreaker) Failed() {}

func (n *circuitBreaker) Succeeded() {}

func (n *circuitBreaker) CanProceed() bool {
	return true
}

func (n *circuitBreaker) CannotProceed() bool {
	return false
}
