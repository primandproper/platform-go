// Package fnv implements hashing.Hasher using the FNV-1a (128-bit) hash.
//
// WARNING: FNV-1a is a NON-CRYPTOGRAPHIC hash. It is fast and useful for hash
// tables and detecting accidental data corruption, but it provides NO security
// guarantees. It MUST NOT be used for password hashing, digital signatures,
// integrity protection against tampering, or any other security-sensitive
// purpose. Use the sha256 or sha512 hashers for those cases.
package fnv

import (
	"encoding/hex"
	"hash/fnv"

	"github.com/primandproper/platform-go/v5/cryptography/hashing"
)

var _ hashing.Hasher = (*fnvHasher)(nil)

type (
	fnvHasher struct{}
)

// NewFNVHasher returns a hashing.Hasher backed by the FNV-1a (128-bit) hash.
//
// WARNING: this is a NON-CRYPTOGRAPHIC hash and MUST NOT be used for security,
// password, or tamper-resistance purposes. See the package doc.
func NewFNVHasher() hashing.Hasher {
	return &fnvHasher{}
}

func (s *fnvHasher) Hash(content string) (string, error) {
	h := fnv.New128a()
	if _, err := h.Write([]byte(content)); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
