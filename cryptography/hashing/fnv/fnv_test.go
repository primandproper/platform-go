package fnv

import (
	"testing"

	"github.com/shoenig/test"
)

func Test_fnvHasher_Hash(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		hasher := NewFNVHasher()

		result, err := hasher.Hash(t.Name())
		test.NoError(t, err)
		test.EqOp(t, "780242af2cb9fb3c85ad54840e9411ec", result)
	})
}
