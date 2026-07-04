package partitioned

import (
	"sync"

	"github.com/primandproper/platform-go/v3/circuitbreaking"
)

// KeyedCircuitBreaker hands out an independent CircuitBreaker per registered key.
type KeyedCircuitBreaker interface {
	// For returns the dedicated breaker registered for key, or the shared global
	// breaker if key was not registered.
	For(key string) circuitbreaking.CircuitBreaker
}

var _ KeyedCircuitBreaker = (*keyedBreaker)(nil)

// keyedBreaker is a KeyedCircuitBreaker backed by a map of dedicated breakers and
// a shared global fallback.
type keyedBreaker struct {
	global   circuitbreaking.CircuitBreaker
	breakers map[string]circuitbreaking.CircuitBreaker
	mu       sync.RWMutex
}

// NewKeyedCircuitBreaker returns a KeyedCircuitBreaker that serves each key in
// breakers from its dedicated CircuitBreaker and any other key from global.
func NewKeyedCircuitBreaker(global circuitbreaking.CircuitBreaker, breakers map[string]circuitbreaking.CircuitBreaker) KeyedCircuitBreaker {
	if breakers == nil {
		breakers = map[string]circuitbreaking.CircuitBreaker{}
	}

	return &keyedBreaker{
		global:   global,
		breakers: breakers,
	}
}

// For returns the dedicated breaker for key, falling back to the global breaker
// when key has no dedicated breaker.
func (k *keyedBreaker) For(key string) circuitbreaking.CircuitBreaker {
	k.mu.RLock()
	cb, ok := k.breakers[key]
	k.mu.RUnlock()

	if ok {
		return cb
	}

	return k.global
}
