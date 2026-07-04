package encryption

import (
	"context"
)

type (
	// MasterKey is the secret key material used to derive encryption/decryption
	// keys. It is a named type over []byte so that dependency-injection lookups
	// resolve it distinctly and cannot collide with an arbitrary []byte value
	// registered in the same container.
	MasterKey []byte

	Encryptor interface {
		Encrypt(ctx context.Context, content string) (string, error)
	}

	Decryptor interface {
		Decrypt(ctx context.Context, content string) (string, error)
	}

	EncryptorDecryptor interface {
		Encryptor
		Decryptor
	}
)
