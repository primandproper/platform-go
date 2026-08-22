package encryption

import (
	"github.com/primandproper/platform-go/v13/errors"
)

var (
	ErrIncorrectKeyLength = errors.New("secret is not the right length")

	// ErrMalformedCiphertext is returned when ciphertext is too short or too
	// damaged to be parsed at all — a truncated frame, a missing nonce, a key
	// ID length that runs off the end. It means the bytes are not a ciphertext
	// this package produced, which is a different problem from a ciphertext
	// that fails to authenticate.
	ErrMalformedCiphertext = errors.New("malformed ciphertext")

	// ErrAuthenticationFailed is returned when ciphertext fails its
	// authentication check. It covers tampering, a wrong key, and associated
	// data that does not match what encryption was given, and it deliberately
	// does not distinguish between them: telling a caller which one it was
	// tells an attacker the same thing.
	ErrAuthenticationFailed = errors.New("ciphertext authentication failed")

	// ErrUnknownKeyID is returned when a ciphertext names a key the ring does
	// not hold. In a rotating system this is the expected shape of a real
	// operational problem — a key retired before everything it encrypted was
	// re-encrypted — so it is worth alerting on rather than swallowing.
	ErrUnknownKeyID = errors.New("ciphertext names a key that is not in the keyring")

	// ErrEmptyKeyring is returned when a Keyring is built with no keys.
	ErrEmptyKeyring = errors.New("keyring contains no keys")

	// ErrNilCipher is returned when a key is offered to a ring with no Cipher
	// to perform its encryption.
	ErrNilCipher = errors.New("key has no cipher")

	// ErrNoCurrentKey is returned when a Keyring's named current key is not
	// among the keys it was given. Encryption has to pick exactly one key and
	// there is no safe way to guess which.
	ErrNoCurrentKey = errors.New("keyring has no current key")

	// ErrEmptyKeyID is returned when a key is offered to a ring without an ID.
	// Every ciphertext has to name its key, so a key with no name cannot
	// participate.
	ErrEmptyKeyID = errors.New("key ID is empty")

	// ErrKeyIDTooLong is returned when a key ID exceeds MaxKeyIDLength.
	ErrKeyIDTooLong = errors.New("key ID is too long")

	// ErrDuplicateKeyID is returned when two keys in one ring share an ID.
	// Which one a ciphertext meant would be unanswerable.
	ErrDuplicateKeyID = errors.New("keyring contains duplicate key IDs")

	// ErrUnsupportedCiphertextVersion is returned when a ciphertext's leading
	// version byte is one this build does not know. It means the data was
	// written by a newer version of this package, and the safe response is to
	// refuse rather than to guess at the layout.
	ErrUnsupportedCiphertextVersion = errors.New("unsupported ciphertext version")
)
