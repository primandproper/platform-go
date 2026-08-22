package sha256

import (
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"

	"github.com/shoenig/test"
)

func TestNewSHA256Hasher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		hasher := NewSHA256Hasher()

		test.EqOp(t, "a1c5d735eb36fbd4c29d560db3fa02f0f7167cace956a08f3d71bc4d9496ad87", hashing.HexString(hasher, t.Name()))
	})

	T.Run("digest is thirty-two bytes wide", func(t *testing.T) {
		t.Parallel()

		test.SliceLen(t, 32, NewSHA256Hasher().Hash([]byte("anything")))
	})
}
