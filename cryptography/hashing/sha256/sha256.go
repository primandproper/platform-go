// Package sha256 implements hashing.Hasher using SHA-256, producing a 32-byte
// digest.
//
// It is a cryptographic hash: finding a second input with the same digest is
// not practical, which is what makes it suitable for content addressing,
// integrity checks, and fingerprints. Two things it is still not. It is
// unkeyed, so anyone can recompute a digest over content they chose — proving
// who computed it needs the hmac sibling. And it is fast, which is the wrong
// property for a stored password; use authentication/argon2 there.
//
// Prefer it over sha512 when digest size matters, and sha512 when it does not:
// on 64-bit hardware SHA-512 is usually the quicker of the two.
package sha256

import (
	"crypto/sha256"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"
)

var _ hashing.Hasher = (*Hasher)(nil)

// Hasher is the SHA-256 hashing.Hasher implementation. It is exported, and
// returned by NewSHA256Hasher, so a caller who has chosen SHA-256 can depend on
// that choice rather than on the interface every digest algorithm shares.
type (
	Hasher struct{}
)

// NewSHA256Hasher returns a hashing.Hasher backed by SHA-256. Code that does
// not need the hashing.Hasher seam should call crypto/sha256 directly; this
// exists so a digest algorithm can be selected at runtime.
func NewSHA256Hasher() *Hasher {
	return &Hasher{}
}

func (s *Hasher) Hash(content []byte) []byte {
	sum := sha256.Sum256(content)

	return sum[:]
}
