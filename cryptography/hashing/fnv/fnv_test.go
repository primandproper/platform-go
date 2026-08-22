package fnv

import (
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"

	"github.com/shoenig/test"
)

func TestNewFNV128aHasher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		hasher := NewFNV128aHasher()

		test.EqOp(t, "a1b752dc545d890a5c2a2589fbd6a796", hashing.HexString(hasher, t.Name()))
	})
}

func TestNewFNV64aHasher(T *testing.T) {
	T.Parallel()

	T.Run("digest is the big-endian encoding of Sum64a", func(t *testing.T) {
		t.Parallel()

		// The two surfaces must not drift: the Hasher is only a rendering of
		// the number Sum64a returns.
		test.EqOp(t, "af63dc4c8601ec8c", hashing.HexString(NewFNV64aHasher(), "a"))
	})

	T.Run("digest is eight bytes wide", func(t *testing.T) {
		t.Parallel()

		test.SliceLen(t, 8, NewFNV64aHasher().Hash([]byte("anything")))
	})
}

func TestSum64a(T *testing.T) {
	T.Parallel()

	T.Run("matches the FNV-1a 64-bit reference vector", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, uint64(0xaf63dc4c8601ec8c), Sum64a([]byte("a")))
	})

	T.Run("is stable across calls", func(t *testing.T) {
		t.Parallel()

		content := []byte("platform-migrations:tenant-a")

		test.EqOp(t, Sum64a(content), Sum64a(content))
	})

	T.Run("distinct inputs give distinct hashes", func(t *testing.T) {
		t.Parallel()

		test.NotEqOp(t, Sum64a([]byte("tenant-a")), Sum64a([]byte("tenant-b")))
	})

	T.Run("empty and nil content agree", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, Sum64a(nil), Sum64a([]byte{}))
	})
}

func TestSum128a(T *testing.T) {
	T.Parallel()

	T.Run("agrees with the 128-bit hasher", func(t *testing.T) {
		t.Parallel()

		content := []byte("a")
		sum := Sum128a(content)

		test.Eq(t, NewFNV128aHasher().Hash(content), sum[:])
	})

	T.Run("differs from the 64-bit hash", func(t *testing.T) {
		t.Parallel()

		test.NotEqOp(t, "af63dc4c8601ec8c", hashing.HexString(NewFNV128aHasher(), "a"))
	})
}
