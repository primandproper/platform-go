package encryption

import (
	"github.com/primandproper/platform-go/errors"
)

var (
	ErrIncorrectKeyLength = errors.New("secret is not the right length")

	// ErrMalformedCiphertext is returned when ciphertext is too short to contain a nonce.
	ErrMalformedCiphertext = errors.New("malformed ciphertext")
)
