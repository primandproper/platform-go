package encryption

import (
	"testing"

	"github.com/shoenig/test"
)

func TestErrIncorrectKeyLength(T *testing.T) {
	T.Parallel()

	T.Run("is not nil", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, ErrIncorrectKeyLength)
		test.EqError(t, ErrIncorrectKeyLength, "secret is not the right length")
	})
}

func TestErrMalformedCiphertext(T *testing.T) {
	T.Parallel()

	T.Run("is not nil", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, ErrMalformedCiphertext)
		test.EqError(t, ErrMalformedCiphertext, "malformed ciphertext")
	})
}
