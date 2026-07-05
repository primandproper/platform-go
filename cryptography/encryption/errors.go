package encryption

import (
	"github.com/primandproper/platform-go/v4/errors"
)

var (
	ErrIncorrectKeyLength = errors.New("secret is not the right length")

	// ErrMalformedCiphertext is returned when ciphertext is too short to contain a nonce.
	ErrMalformedCiphertext = errors.New("malformed ciphertext")

	// ErrAuthenticationFailed is returned when ciphertext fails its authentication check.
	ErrAuthenticationFailed = errors.New("ciphertext authentication failed")
)
