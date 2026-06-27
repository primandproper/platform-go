package aes

import (
	"testing"

	"github.com/primandproper/platform-go/observability"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"
	"github.com/primandproper/platform-go/random"

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

		actual, err := encryptor.Decrypt(ctx, encrypted)
		test.NoError(t, err)
		test.EqOp(t, expected, actual)
	})

	T.Run("observes content on encrypt", func(t *testing.T) {
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
			"content": expected,
		})
	})

	T.Run("observes content and records error on bad decrypt", func(t *testing.T) {
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
			"content": badContent,
		})
		must.SliceLen(t, 1, op.Errors)
	})
}
