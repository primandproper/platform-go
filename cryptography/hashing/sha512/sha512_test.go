package sha512

import (
	"testing"

	"github.com/shoenig/test"
)

func Test_sha512Hasher_Hash(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		hasher := NewSHA512Hasher()

		result, err := hasher.Hash(t.Name())
		test.NoError(t, err)
		test.EqOp(t, "5928cb042c3cc8dc19dce0eb7caa4ad440e7c4b429503c42ef2fa3dc0fee9232a85db9276c690809f70c92ea68deb255bbd5dd1e9ecd71ade0db9eaaab205c21", result)
	})
}
