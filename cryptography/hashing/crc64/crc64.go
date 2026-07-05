// Package crc64 implements hashing.Hasher using the CRC-64 (ISO) checksum.
//
// WARNING: CRC-64 is a NON-CRYPTOGRAPHIC checksum. It is fast and useful for
// detecting accidental data corruption, but it provides NO security guarantees.
// It MUST NOT be used for password hashing, digital signatures, integrity
// protection against tampering, or any other security-sensitive purpose. Use
// the sha256 or sha512 hashers for those cases.
package crc64

import (
	"encoding/hex"
	"hash/crc64"

	"github.com/primandproper/platform-go/v4/cryptography/hashing"
)

var _ hashing.Hasher = (*crc64Hasher)(nil)

type (
	crc64Hasher struct{}
)

// NewCRC64Hasher returns a hashing.Hasher backed by the CRC-64 (ISO) checksum.
//
// WARNING: this is a NON-CRYPTOGRAPHIC checksum and MUST NOT be used for
// security, password, or tamper-resistance purposes. See the package doc.
func NewCRC64Hasher() hashing.Hasher {
	return &crc64Hasher{}
}

func (s *crc64Hasher) Hash(content string) (string, error) {
	h := crc64.New(crc64.MakeTable(crc64.ISO))
	if _, err := h.Write([]byte(content)); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
