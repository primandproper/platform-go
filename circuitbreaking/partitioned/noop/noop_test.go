package noop

import (
	"testing"

	"github.com/shoenig/test"
)

func TestNewKeyedCircuitBreaker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		x := NewKeyedCircuitBreaker()
		test.NotNil(t, x)
	})
}

func TestKeyedCircuitBreaker_For(T *testing.T) {
	T.Parallel()

	T.Run("always proceeds for any key", func(t *testing.T) {
		t.Parallel()

		x := NewKeyedCircuitBreaker()

		cb := x.For("123")
		test.NotNil(t, cb)
		test.True(t, cb.CanProceed())

		cb.Failed()
		test.True(t, cb.CanProceed())
		test.False(t, cb.CannotProceed())
	})
}
