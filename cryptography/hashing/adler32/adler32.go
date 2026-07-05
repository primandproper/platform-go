// Package adler32 implements hashing.Hasher using the Adler-32 checksum.
//
// WARNING: Adler-32 is a NON-CRYPTOGRAPHIC checksum. It is fast and useful for
// detecting accidental data corruption, but it provides NO security guarantees.
// It MUST NOT be used for password hashing, digital signatures, integrity
// protection against tampering, or any other security-sensitive purpose. Use
// the sha256 or sha512 hashers for those cases.
package adler32

import (
	"encoding/hex"
	"hash/adler32"

	"github.com/primandproper/platform-go/v4/cryptography/hashing"
)

var _ hashing.Hasher = (*adler32Hasher)(nil)

type (
	adler32Hasher struct{}
)

// NewAdler32Hasher returns a hashing.Hasher backed by the Adler-32 checksum.
//
// WARNING: this is a NON-CRYPTOGRAPHIC checksum and MUST NOT be used for
// security, password, or tamper-resistance purposes. See the package doc.
func NewAdler32Hasher() hashing.Hasher {
	return &adler32Hasher{}
}

func (s *adler32Hasher) Hash(content string) (string, error) {
	h := adler32.New()
	if _, err := h.Write([]byte(content)); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
