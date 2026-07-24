package partitioned

import (
	"testing"

	"github.com/primandproper/platform-go/v6/circuitbreaking"
	cbmock "github.com/primandproper/platform-go/v6/circuitbreaking/mock"

	"github.com/shoenig/test"
)

func TestNewKeyedCircuitBreaker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		x := NewKeyedCircuitBreaker(&cbmock.CircuitBreakerMock{}, nil)
		test.NotNil(t, x)
	})

	T.Run("with nil breakers falls back to global for every key", func(t *testing.T) {
		t.Parallel()

		global := &cbmock.CircuitBreakerMock{}
		x := NewKeyedCircuitBreaker(global, nil)

		test.True(t, x.For("anything") == global)
	})
}

func TestKeyedBreaker_For(T *testing.T) {
	T.Parallel()

	T.Run("returns dedicated breaker for a registered key", func(t *testing.T) {
		t.Parallel()

		dedicated := &cbmock.CircuitBreakerMock{}
		global := &cbmock.CircuitBreakerMock{}

		x := NewKeyedCircuitBreaker(global, map[string]circuitbreaking.CircuitBreaker{"123": dedicated})

		test.True(t, x.For("123") == dedicated)
	})

	T.Run("falls back to global for an unregistered key", func(t *testing.T) {
		t.Parallel()

		dedicated := &cbmock.CircuitBreakerMock{}
		global := &cbmock.CircuitBreakerMock{}

		x := NewKeyedCircuitBreaker(global, map[string]circuitbreaking.CircuitBreaker{"123": dedicated})

		test.True(t, x.For("456") == global)
	})

	// isolation is the core behavior: a broken key is blocked while other keys, served
	// by the healthy global breaker, still proceed.
	T.Run("breaks one key in isolation", func(t *testing.T) {
		t.Parallel()

		broken := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}
		global := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
		}

		x := NewKeyedCircuitBreaker(global, map[string]circuitbreaking.CircuitBreaker{"123": broken})

		test.True(t, x.For("123").CannotProceed())  // heavy tenant is circuit broken
		test.False(t, x.For("456").CannotProceed()) // small tenant still proceeds
	})
}
