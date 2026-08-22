package aes

import (
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// key is 32 bytes, which is what AES-256 wants.
var key = []byte("0123456789abcdef0123456789abcdef")

func newCipher(t *testing.T) encryption.Cipher {
	t.Helper()

	c, err := NewCipher(key)
	must.NoError(t, err)

	return c
}

func TestNewCipher(T *testing.T) {
	T.Parallel()

	T.Run("builds from a 32 byte key", func(t *testing.T) {
		t.Parallel()

		c, err := NewCipher(key)
		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("rejects a short key", func(t *testing.T) {
		t.Parallel()

		_, err := NewCipher([]byte("too short"))
		test.ErrorIs(t, err, encryption.ErrIncorrectKeyLength)
	})

	T.Run("rejects a 16 byte key", func(t *testing.T) {
		t.Parallel()

		// AES-128 is a valid cipher and not what this package offers. A key
		// that is accidentally half the intended length should fail loudly
		// rather than quietly halve the security margin.
		_, err := NewCipher(make([]byte, 16))
		test.ErrorIs(t, err, encryption.ErrIncorrectKeyLength)
	})

	T.Run("rejects an empty key", func(t *testing.T) {
		t.Parallel()

		_, err := NewCipher(nil)
		test.ErrorIs(t, err, encryption.ErrIncorrectKeyLength)
	})
}

func TestCipher_RoundTrip(T *testing.T) {
	T.Parallel()

	T.Run("seals and opens", func(t *testing.T) {
		t.Parallel()

		c := newCipher(t)

		sealed, err := c.Seal(t.Context(), []byte("secret"), nil)
		must.NoError(t, err)

		opened, err := c.Open(t.Context(), sealed, nil)
		must.NoError(t, err)

		test.Eq(t, []byte("secret"), opened)
	})

	T.Run("binds associated data", func(t *testing.T) {
		t.Parallel()

		c := newCipher(t)

		sealed, err := c.Seal(t.Context(), []byte("secret"), []byte("row-7"))
		must.NoError(t, err)

		opened, err := c.Open(t.Context(), sealed, []byte("row-7"))
		must.NoError(t, err)

		test.Eq(t, []byte("secret"), opened)
	})

	T.Run("round trips binary plaintext", func(t *testing.T) {
		t.Parallel()

		// The byte surface exists so that compressed or otherwise non-UTF-8
		// payloads survive without an encoding step.
		c := newCipher(t)

		plaintext := []byte{0x00, 0xff, 0xfe, 0x80, 0x01}

		sealed, err := c.Seal(t.Context(), plaintext, nil)
		must.NoError(t, err)

		opened, err := c.Open(t.Context(), sealed, nil)
		must.NoError(t, err)

		test.Eq(t, plaintext, opened)
	})

	T.Run("round trips an empty plaintext", func(t *testing.T) {
		t.Parallel()

		c := newCipher(t)

		sealed, err := c.Seal(t.Context(), nil, nil)
		must.NoError(t, err)

		opened, err := c.Open(t.Context(), sealed, nil)
		must.NoError(t, err)

		test.SliceEmpty(t, opened)
	})

	T.Run("a fresh nonce makes each sealing distinct", func(t *testing.T) {
		t.Parallel()

		c := newCipher(t)

		first, err := c.Seal(t.Context(), []byte("secret"), nil)
		must.NoError(t, err)

		second, err := c.Seal(t.Context(), []byte("secret"), nil)
		must.NoError(t, err)

		// Identical plaintext under identical key must not produce identical
		// bytes, or equal values become visibly equal at rest.
		test.NotEq(t, first, second)
	})
}

func TestCipher_Authentication(T *testing.T) {
	T.Parallel()

	T.Run("mismatched associated data fails", func(t *testing.T) {
		t.Parallel()

		c := newCipher(t)

		sealed, err := c.Seal(t.Context(), []byte("secret"), []byte("row-7"))
		must.NoError(t, err)

		_, err = c.Open(t.Context(), sealed, []byte("row-8"))
		test.ErrorIs(t, err, encryption.ErrAuthenticationFailed)
	})

	T.Run("omitted associated data fails", func(t *testing.T) {
		t.Parallel()

		c := newCipher(t)

		sealed, err := c.Seal(t.Context(), []byte("secret"), []byte("row-7"))
		must.NoError(t, err)

		_, err = c.Open(t.Context(), sealed, nil)
		test.ErrorIs(t, err, encryption.ErrAuthenticationFailed)
	})

	T.Run("a tampered ciphertext fails", func(t *testing.T) {
		t.Parallel()

		c := newCipher(t)

		sealed, err := c.Seal(t.Context(), []byte("secret"), nil)
		must.NoError(t, err)

		sealed[len(sealed)-1] ^= 0xff

		_, err = c.Open(t.Context(), sealed, nil)
		test.ErrorIs(t, err, encryption.ErrAuthenticationFailed)
	})

	T.Run("the wrong key fails", func(t *testing.T) {
		t.Parallel()

		c := newCipher(t)

		sealed, err := c.Seal(t.Context(), []byte("secret"), nil)
		must.NoError(t, err)

		other, err := NewCipher([]byte("fedcba9876543210fedcba9876543210"))
		must.NoError(t, err)

		// Indistinguishable from tampering on purpose: telling the caller
		// which one it was tells an attacker the same thing.
		_, err = other.Open(t.Context(), sealed, nil)
		test.ErrorIs(t, err, encryption.ErrAuthenticationFailed)
	})

	T.Run("a ciphertext shorter than a nonce is malformed", func(t *testing.T) {
		t.Parallel()

		c := newCipher(t)

		_, err := c.Open(t.Context(), []byte{0x01, 0x02}, nil)
		test.ErrorIs(t, err, encryption.ErrMalformedCiphertext)
	})
}

// exhaustedReader stands in for an entropy source that has stopped answering.
type exhaustedReader struct{}

func (exhaustedReader) Read([]byte) (int, error) { return 0, errNoEntropy }

var errNoEntropy = errors.New("entropy source is exhausted")

func TestCipher_NonceFailure(T *testing.T) {
	T.Parallel()

	T.Run("a nonce that cannot be generated fails the seal", func(t *testing.T) {
		t.Parallel()

		// Sealing without a fresh nonce would reuse one, and nonce reuse under
		// GCM is not a degraded mode — it leaks the authentication key. There
		// is no sensible fallback, so this has to be a hard failure.
		c, err := NewCipher(key)
		must.NoError(t, err)

		c.random = exhaustedReader{}

		_, err = c.Seal(t.Context(), []byte("secret"), nil)
		test.ErrorIs(t, err, errNoEntropy)
	})
}
