package adler32

import (
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"

	"github.com/shoenig/test"
)

func TestNewAdler32Hasher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		hasher := NewAdler32Hasher()

		test.EqOp(t, "a2770af3", hashing.HexString(hasher, t.Name()))
	})

	T.Run("digest is four bytes wide", func(t *testing.T) {
		t.Parallel()

		test.SliceLen(t, 4, NewAdler32Hasher().Hash([]byte("anything")))
	})

	T.Run("empty and nil content agree", func(t *testing.T) {
		t.Parallel()

		hasher := NewAdler32Hasher()

		test.Eq(t, hasher.Hash(nil), hasher.Hash([]byte{}))
	})
}
