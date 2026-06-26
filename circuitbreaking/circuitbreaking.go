package circuitbreaking

import (
	"github.com/primandproper/platform-go/errors"
)

// ErrCircuitBroken is returned when a circuit breaker has tripped.
var ErrCircuitBroken = errors.New("service circuit broken")

// CircuitBreaker tracks failures and successes to determine whether an operation should proceed.
type CircuitBreaker interface {
	Failed()
	Succeeded()
	CanProceed() bool
	CannotProceed() bool
}
