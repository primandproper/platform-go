// Package sha512 implements hashing.Hasher using SHA-512, producing a 64-byte
// digest.
//
// It is a cryptographic hash: finding a second input with the same digest is
// not practical, which is what makes it suitable for content addressing,
// integrity checks, and fingerprints. Two things it is still not. It is
// unkeyed, so anyone can recompute a digest over content they chose — proving
// who computed it needs the hmac sibling. And it is fast, which is the wrong
// property for a stored password; use authentication/argon2 there.
//
// The digest is twice the width of the sha256 sibling's, and on 64-bit hardware
// is usually computed faster. Prefer sha256 only where the extra 32 bytes per
// digest are worth having back.
package sha512

import (
	"crypto/sha512"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"
)

var _ hashing.Hasher = (*Hasher)(nil)

// Hasher is the SHA-512 hashing.Hasher implementation. It is exported, and
// returned by NewSHA512Hasher, so a caller who has chosen SHA-512 can depend on
// that choice rather than on the interface every digest algorithm shares.
type (
	Hasher struct{}
)

// NewSHA512Hasher returns a hashing.Hasher backed by SHA-512. Code that does
// not need the hashing.Hasher seam should call crypto/sha512 directly; this
// exists so a digest algorithm can be selected at runtime.
func NewSHA512Hasher() *Hasher {
	return &Hasher{}
}

func (s *Hasher) Hash(content []byte) []byte {
	sum := sha512.Sum512(content)

	return sum[:]
}
