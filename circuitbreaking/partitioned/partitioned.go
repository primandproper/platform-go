package partitioned

import (
	"sync"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
)

// KeyedCircuitBreaker hands out an independent CircuitBreaker per registered key.
type KeyedCircuitBreaker interface {
	// For returns the dedicated breaker registered for key, or the shared global
	// breaker if key was not registered.
	For(key string) circuitbreaking.CircuitBreaker
}

var _ KeyedCircuitBreaker = (*KeyedBreaker)(nil)

// KeyedBreaker is a KeyedCircuitBreaker backed by a map of dedicated breakers and
// a shared global fallback. It is exported, and returned by
// NewKeyedCircuitBreaker, so a caller can depend on the breaker it built rather
// than on the KeyedCircuitBreaker seam.
type KeyedBreaker struct {
	global   circuitbreaking.CircuitBreaker
	breakers map[string]circuitbreaking.CircuitBreaker
	mu       sync.RWMutex
}

// NewKeyedCircuitBreaker returns a KeyedCircuitBreaker that serves each key in
// breakers from its dedicated CircuitBreaker and any other key from global.
func NewKeyedCircuitBreaker(global circuitbreaking.CircuitBreaker, breakers map[string]circuitbreaking.CircuitBreaker) *KeyedBreaker {
	if breakers == nil {
		breakers = map[string]circuitbreaking.CircuitBreaker{}
	}

	return &KeyedBreaker{
		global:   global,
		breakers: breakers,
	}
}

// For returns the dedicated breaker for key, falling back to the global breaker
// when key has no dedicated breaker.
func (k *KeyedBreaker) For(key string) circuitbreaking.CircuitBreaker {
	k.mu.RLock()
	cb, ok := k.breakers[key]
	k.mu.RUnlock()

	if ok {
		return cb
	}

	return k.global
}
