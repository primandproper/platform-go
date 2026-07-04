package hashing

type (
	// Hasher hashes a string into a hex-encoded digest.
	//
	// NOTE: implementations of this interface vary in cryptographic strength.
	// The sha256 and sha512 implementations are cryptographic hashes; the
	// adler32, crc64, and fnv implementations are NON-CRYPTOGRAPHIC checksums
	// and MUST NOT be selected for security, password, or tamper-resistance
	// purposes. Choose the implementation deliberately.
	Hasher interface {
		Hash(content string) (string, error)
	}
)
