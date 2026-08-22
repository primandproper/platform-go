// Package noop is the circuitbreaking.CircuitBreaker that never opens:
// CanProceed is always true, no matter how many failures are reported to it.
//
// Choosing it removes the backstop, not merely the bookkeeping. A dependency
// that is down receives the full request load for as long as it stays down, and
// every caller waits out its own timeout instead of failing fast on an open
// circuit. That is the right trade where the protected call is local, or where
// tripping would cost more than retrying.
//
// It is also handed out rather than named in one place:
// circuitbreaking/config.EnsureCircuitBreaker substitutes it for a nil breaker.
// Unlike the observability Ensure* helpers, which resolve to their noops
// silently because unmetered is a normal state, that one logs when it does —
// a component that believes it is protected and is not is the failure mode this
// package exists to prevent, so pass it a logger and you will hear about it.
package noop

import (
	"github.com/primandproper/platform-go/v13/circuitbreaking"
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
