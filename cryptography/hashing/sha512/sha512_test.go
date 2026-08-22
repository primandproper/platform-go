package sha512

import (
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"

	"github.com/shoenig/test"
)

func TestNewSHA512Hasher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		hasher := NewSHA512Hasher()

		test.EqOp(t, "234611c57cb7c803c7b990fab3de4d0c5734ae877452e2b4951e595cb8fcbcbd8ca39d4e37b74e0370947851e757189827293d9955588bb10f278aec87cb96ba", hashing.HexString(hasher, t.Name()))
	})

	T.Run("digest is sixty-four bytes wide", func(t *testing.T) {
		t.Parallel()

		test.SliceLen(t, 64, NewSHA512Hasher().Hash([]byte("anything")))
	})
}
