package adler32

import (
	"testing"

	"github.com/shoenig/test"
)

func Test_adler32Hasher_Hash(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		hasher := NewAdler32Hasher()

		result, err := hasher.Hash(t.Name())
		test.NoError(t, err)
		test.EqOp(t, "c7060c2b", result)
	})
}
