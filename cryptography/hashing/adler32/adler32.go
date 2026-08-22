// Package adler32 implements hashing.Hasher using the Adler-32 checksum.
//
// There is deliberately no package-level Checksum function here: the standard
// library's hash/adler32.Checksum already returns the uint32 directly, and
// re-exporting it would add a name without adding anything else.
//
// WARNING: Adler-32 is a NON-CRYPTOGRAPHIC checksum. It is fast and useful for
// detecting accidental data corruption, but it provides NO security guarantees.
// It MUST NOT be used for password hashing, digital signatures, integrity
// protection against tampering, or any other security-sensitive purpose. Use
// the sha256 or sha512 hashers for those cases.
package adler32

import (
	"encoding/binary"
	"hash/adler32"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"
)

var _ hashing.Hasher = (*Hasher)(nil)

// Hasher is the Adler-32 hashing.Hasher implementation. It is exported, and
// returned by NewAdler32Hasher, so a caller who has chosen Adler-32 can depend
// on that choice rather than on the interface every digest algorithm shares.
type (
	Hasher struct{}
)

// NewAdler32Hasher returns a hashing.Hasher backed by the Adler-32 checksum,
// rendered big-endian so the digest bytes read in the same order as the hex
// string. Callers that want the checksum as a number should call
// hash/adler32.Checksum directly.
//
// WARNING: this is a NON-CRYPTOGRAPHIC checksum and MUST NOT be used for
// security, password, or tamper-resistance purposes. See the package doc.
func NewAdler32Hasher() *Hasher {
	return &Hasher{}
}

func (s *Hasher) Hash(content []byte) []byte {
	return binary.BigEndian.AppendUint32(nil, adler32.Checksum(content))
}
