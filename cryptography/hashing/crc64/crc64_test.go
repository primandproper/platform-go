package crc64

import (
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"

	"github.com/shoenig/test"
)

func TestNewCRC64ISOHasher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		hasher := NewCRC64ISOHasher()

		test.EqOp(t, "11f54c2a1d3ef986", hashing.HexString(hasher, t.Name()))
	})

	T.Run("digest is eight bytes wide", func(t *testing.T) {
		t.Parallel()

		test.SliceLen(t, 8, NewCRC64ISOHasher().Hash([]byte("anything")))
	})
}

func TestChecksumISO(T *testing.T) {
	T.Parallel()

	T.Run("matches the ISO polynomial", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, uint64(0x3420000000000000), ChecksumISO([]byte("a")))
	})

	T.Run("digest is the big-endian encoding of the checksum", func(t *testing.T) {
		t.Parallel()

		// The two surfaces must not drift: the Hasher is only a rendering of
		// the number ChecksumISO returns.
		test.EqOp(t, "3420000000000000", hashing.HexString(NewCRC64ISOHasher(), "a"))
	})

	T.Run("distinct inputs give distinct checksums", func(t *testing.T) {
		t.Parallel()

		test.NotEqOp(t, ChecksumISO([]byte("tenant-a")), ChecksumISO([]byte("tenant-b")))
	})

	T.Run("empty and nil content agree", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, ChecksumISO(nil), ChecksumISO([]byte{}))
	})
}
