// Package crc64 implements hashing.Hasher using the CRC-64 (ISO) checksum,
// and exposes ChecksumISO for callers that want the checksum as the integer
// it natively is.
//
// WARNING: CRC-64 is a NON-CRYPTOGRAPHIC checksum. It is fast and useful for
// detecting accidental data corruption, but it provides NO security guarantees.
// It MUST NOT be used for password hashing, digital signatures, integrity
// protection against tampering, or any other security-sensitive purpose. Use
// the sha256 or sha512 hashers for those cases.
package crc64

import (
	"encoding/binary"
	"hash/crc64"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"
)

// isoTable is built once at package load. Building a CRC table is not free,
// and it depends only on the polynomial, so per-call construction is pure
// waste.
var isoTable = crc64.MakeTable(crc64.ISO)

var _ hashing.Hasher = (*ISOHasher)(nil)

// ISOHasher is the CRC-64 (ISO) hashing.Hasher implementation. The polynomial
// is in the name for the reason NewCRC64ISOHasher gives: ISO and ECMA are both
// in wide use and disagree on the same input. It is exported, and returned by
// NewCRC64ISOHasher, so a caller who has chosen this polynomial can depend on
// that choice rather than on the interface every digest algorithm shares.
type (
	ISOHasher struct{}
)

// NewCRC64ISOHasher returns a hashing.Hasher backed by the CRC-64 (ISO)
// checksum, rendered big-endian so the digest bytes read in the same order as
// the hex string. Callers that want the checksum as a number should use
// ChecksumISO instead of decoding this.
//
// The polynomial is named because it is a choice: ISO and ECMA are both in
// wide use and produce different checksums for the same input.
//
// WARNING: this is a NON-CRYPTOGRAPHIC checksum and MUST NOT be used for
// security, password, or tamper-resistance purposes. See the package doc.
func NewCRC64ISOHasher() *ISOHasher {
	return &ISOHasher{}
}

func (s *ISOHasher) Hash(content []byte) []byte {
	return binary.BigEndian.AppendUint64(nil, ChecksumISO(content))
}

// ChecksumISO returns the CRC-64 (ISO) checksum of content as the uint64 it
// natively is, for bucketing, sharding, and advisory-lock IDs. It allocates
// nothing.
//
// WARNING: this is a NON-CRYPTOGRAPHIC checksum. See the package doc.
func ChecksumISO(content []byte) uint64 {
	return crc64.Checksum(content, isoTable)
}
