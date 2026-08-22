// Package fnv implements hashing.Hasher using the FNV-1a hash, and exposes
// Sum64a and Sum128a for callers that want the hash as the integer it natively
// is. The standard library's hash/fnv has no package-level sum function, so
// those exist to spare every caller the New/Write/Sum dance.
//
// WARNING: FNV-1a is a NON-CRYPTOGRAPHIC hash. It is fast and useful for hash
// tables and detecting accidental data corruption, but it provides NO security
// guarantees. It MUST NOT be used for password hashing, digital signatures,
// integrity protection against tampering, or any other security-sensitive
// purpose. Use the sha256 or sha512 hashers for those cases.
package fnv

import (
	"encoding/binary"
	"hash/fnv"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"
)

var (
	_ hashing.Hasher = (*Hasher64a)(nil)
	_ hashing.Hasher = (*Hasher128a)(nil)
)

// Hasher64a and Hasher128a are the FNV-1a hashing.Hasher implementations, 64-
// and 128-bit respectively. They are exported, and returned by NewFNV64aHasher
// and NewFNV128aHasher, so a caller who has chosen a width can depend on that
// choice rather than on the interface every digest algorithm shares.
type (
	Hasher64a  struct{}
	Hasher128a struct{}
)

// NewFNV64aHasher returns a hashing.Hasher backed by the FNV-1a (64-bit) hash,
// rendered big-endian so the digest bytes read in the same order as the hex
// string. 64 bits is the usual choice for FNV; callers that want the number
// rather than a digest should use Sum64a.
//
// WARNING: this is a NON-CRYPTOGRAPHIC hash and MUST NOT be used for security,
// password, or tamper-resistance purposes. See the package doc.
func NewFNV64aHasher() *Hasher64a {
	return &Hasher64a{}
}

func (s *Hasher64a) Hash(content []byte) []byte {
	return binary.BigEndian.AppendUint64(nil, Sum64a(content))
}

// NewFNV128aHasher returns a hashing.Hasher backed by the FNV-1a (128-bit)
// hash. Prefer NewFNV64aHasher unless a 16-byte digest is specifically
// required: the extra width buys nothing for bucketing, which is what a
// non-cryptographic hash is for.
//
// WARNING: this is a NON-CRYPTOGRAPHIC hash and MUST NOT be used for security,
// password, or tamper-resistance purposes. See the package doc.
func NewFNV128aHasher() *Hasher128a {
	return &Hasher128a{}
}

func (s *Hasher128a) Hash(content []byte) []byte {
	sum := Sum128a(content)

	return sum[:]
}

// Sum64a returns the FNV-1a (64-bit) hash of content as the uint64 it natively
// is, for bucketing, sharding, and advisory-lock IDs.
//
// WARNING: this is a NON-CRYPTOGRAPHIC hash. See the package doc.
func Sum64a(content []byte) uint64 {
	h := fnv.New64a()
	// hash.Hash guarantees Write never returns an error.
	_, _ = h.Write(content)

	return h.Sum64()
}

// Sum128a returns the FNV-1a (128-bit) hash of content. hash/fnv exposes the
// 128-bit variant only through hash.Hash, so unlike Sum64a there is no integer
// form to return.
//
// WARNING: this is a NON-CRYPTOGRAPHIC hash. See the package doc.
func Sum128a(content []byte) [16]byte {
	h := fnv.New128a()
	// hash.Hash guarantees Write never returns an error.
	_, _ = h.Write(content)

	var out [16]byte
	copy(out[:], h.Sum(nil))

	return out
}
