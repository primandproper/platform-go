package aes

import (
	"encoding/base64"
	"testing"

	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"
	"github.com/primandproper/platform-go/v5/random"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRecordingEncryptor builds an aesImpl with a RecordingObserver swapped in, so
// a test can drive Encrypt/Decrypt and assert which fields it observed.
func newRecordingEncryptor(t *testing.T, key []byte) (*aesImpl, *observability.RecordingObserver) {
	t.Helper()

	ed, err := NewEncryptorDecryptor(tracingnoop.NewTracerProvider(), loggingnoop.NewLogger(), key)
	must.NotNil(t, ed)
	must.NoError(t, err)

	impl, ok := ed.(*aesImpl)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	impl.o11y = obs

	return impl, obs
}

func TestStandardEncryptor(T *testing.T) {
	T.Parallel()

	T.Run("basic operation", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		expected := t.Name()
		secret, err := random.GenerateHexEncodedString(ctx, 16)
		must.NoError(t, err)

		encryptor, err := NewEncryptorDecryptor(tracingnoop.NewTracerProvider(), loggingnoop.NewLogger(), []byte(secret))
		must.NotNil(t, encryptor)
		must.NoError(t, err)

		encrypted, err := encryptor.Encrypt(ctx, expected)
		test.NoError(t, err)
		test.NotEq(t, "", encrypted)

		// The ciphertext must not be the plaintext (guards against an identity "encryptor").
		test.NotEqOp(t, expected, encrypted)

		encrypted2, err := encryptor.Encrypt(ctx, expected)
		test.NoError(t, err)
		test.NotEq(t, "", encrypted2)

		// AES-GCM uses a fresh random nonce per call, so encrypting the same
		// plaintext twice must produce different ciphertexts.
		test.NotEqOp(t, encrypted, encrypted2)

		actual, err := encryptor.Decrypt(ctx, encrypted)
		test.NoError(t, err)
		test.EqOp(t, expected, actual)

		actual2, err := encryptor.Decrypt(ctx, encrypted2)
		test.NoError(t, err)
		test.EqOp(t, expected, actual2)
	})

	T.Run("decrypt rejects tampered ciphertext", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		secret, err := random.GenerateHexEncodedString(ctx, 16)
		must.NoError(t, err)

		encryptor, err := NewEncryptorDecryptor(tracingnoop.NewTracerProvider(), loggingnoop.NewLogger(), []byte(secret))
		must.NoError(t, err)

		encrypted, err := encryptor.Encrypt(ctx, "sensitive payload")
		must.NoError(t, err)

		raw, err := base64.URLEncoding.DecodeString(encrypted)
		must.NoError(t, err)

		// Flip a bit in the authenticated ciphertext body (past the nonce);
		// GCM's tag check must reject it.
		raw[len(raw)-1] ^= 0x01
		tampered := base64.URLEncoding.EncodeToString(raw)

		_, err = encryptor.Decrypt(ctx, tampered)
		must.Error(t, err)
	})

	T.Run("observes content length on encrypt", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		expected := t.Name()
		secret, err := random.GenerateHexEncodedString(ctx, 16)
		must.NoError(t, err)

		encryptor, obs := newRecordingEncryptor(t, []byte(secret))

		encrypted, err := encryptor.Encrypt(ctx, expected)
		must.NoError(t, err)
		must.NotEq(t, "", encrypted)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.LengthKey: len(expected),
		})
	})

	T.Run("observes content length and records error on bad decrypt", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		secret, err := random.GenerateHexEncodedString(ctx, 16)
		must.NoError(t, err)

		encryptor, obs := newRecordingEncryptor(t, []byte(secret))

		// Not valid base64, so decoding fails early.
		const badContent = "!!!not-base64!!!"
		_, err = encryptor.Decrypt(ctx, badContent)
		must.Error(t, err)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.LengthKey: len(badContent),
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("decrypt with ciphertext too short for nonce records error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		secret, err := random.GenerateHexEncodedString(ctx, 16)
		must.NoError(t, err)

		encryptor, obs := newRecordingEncryptor(t, []byte(secret))

		// valid base64 that decodes to fewer than the GCM nonce size.
		tooShort := base64.URLEncoding.EncodeToString([]byte{0, 1, 2})
		_, err = encryptor.Decrypt(ctx, tooShort)
		must.Error(t, err)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.LengthKey: len(tooShort),
		})
		must.SliceLen(t, 1, op.Errors)
	})
}
