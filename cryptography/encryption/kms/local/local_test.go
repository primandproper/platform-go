package local

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/aes"
	"github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var wrappingKey = []byte("0123456789abcdef0123456789abcdef")

func newWrapper(t *testing.T) encryption.KeyWrapper {
	t.Helper()

	cipher, err := aes.NewCipher(wrappingKey)
	must.NoError(t, err)

	w, err := NewKeyWrapper(cipher)
	must.NoError(t, err)

	return w
}

func TestNewKeyWrapper(T *testing.T) {
	T.Parallel()

	T.Run("builds over a cipher", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, newWrapper(t))
	})

	T.Run("rejects a nil cipher", func(t *testing.T) {
		t.Parallel()

		_, err := NewKeyWrapper(nil)
		test.ErrorIs(t, err, encryption.ErrNilCipher)
	})
}

func TestLocalWrapper_RoundTrip(T *testing.T) {
	T.Parallel()

	T.Run("wraps and unwraps a data key", func(t *testing.T) {
		t.Parallel()

		w := newWrapper(t)

		dataKey := []byte("fedcba9876543210fedcba9876543210")

		wrapped, err := w.Wrap(t.Context(), dataKey, nil)
		must.NoError(t, err)

		test.NotEq(t, dataKey, wrapped)

		unwrapped, err := w.Unwrap(t.Context(), wrapped, nil)
		must.NoError(t, err)

		test.Eq(t, dataKey, unwrapped)
	})

	T.Run("binds associated data", func(t *testing.T) {
		t.Parallel()

		w := newWrapper(t)

		wrapped, err := w.Wrap(t.Context(), []byte("data-key"), []byte("subject-42"))
		must.NoError(t, err)

		unwrapped, err := w.Unwrap(t.Context(), wrapped, []byte("subject-42"))
		must.NoError(t, err)

		test.Eq(t, []byte("data-key"), unwrapped)
	})

	T.Run("a wrapped key does not move between subjects", func(t *testing.T) {
		t.Parallel()

		// The reason a per-subject key wrapper binds the subject: otherwise a
		// wrapped key copied into another subject's row unwraps cleanly, and
		// crypto-shredding one subject leaves the other reading their data.
		w := newWrapper(t)

		wrapped, err := w.Wrap(t.Context(), []byte("data-key"), []byte("subject-42"))
		must.NoError(t, err)

		_, err = w.Unwrap(t.Context(), wrapped, []byte("subject-43"))
		test.ErrorIs(t, err, encryption.ErrAuthenticationFailed)
	})

	T.Run("a tampered wrapped key fails", func(t *testing.T) {
		t.Parallel()

		w := newWrapper(t)

		wrapped, err := w.Wrap(t.Context(), []byte("data-key"), nil)
		must.NoError(t, err)

		wrapped[len(wrapped)-1] ^= 0xff

		_, err = w.Unwrap(t.Context(), wrapped, nil)
		test.ErrorIs(t, err, encryption.ErrAuthenticationFailed)
	})
}

// failingCipher refuses in both directions.
type failingCipher struct{}

func (failingCipher) Seal(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errCipher
}

func (failingCipher) Open(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errCipher
}

var errCipher = errors.New("the cipher is unavailable")

func TestLocalWrapper_CipherFailures(T *testing.T) {
	T.Parallel()

	T.Run("a cipher that cannot seal fails the wrap", func(t *testing.T) {
		t.Parallel()

		w, err := NewKeyWrapper(failingCipher{})
		must.NoError(t, err)

		_, err = w.Wrap(t.Context(), []byte("data-key"), nil)
		test.ErrorIs(t, err, errCipher)
	})

	T.Run("a cipher that cannot open fails the unwrap", func(t *testing.T) {
		t.Parallel()

		w, err := NewKeyWrapper(failingCipher{})
		must.NoError(t, err)

		_, err = w.Unwrap(t.Context(), []byte("wrapped"), nil)
		test.ErrorIs(t, err, errCipher)
	})
}
