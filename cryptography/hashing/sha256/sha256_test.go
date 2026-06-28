package sha256

import (
	"testing"

	"github.com/shoenig/test"
)

func Test_sha256Hasher_Hash(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		hasher := NewSHA256Hasher()

		result, err := hasher.Hash(t.Name())
		test.NoError(t, err)
		test.EqOp(t, "f469799cfc8eb5c3fa03e2ec4faf3c1b9a4c3a1c0ac3557a2f963e598cea695f", result)
	})
}
