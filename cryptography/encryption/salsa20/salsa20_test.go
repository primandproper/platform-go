package salsa20

import (
	"testing"

	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"
	"github.com/primandproper/platform-go/random"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRecordingEncryptor builds a salsa20Impl with a RecordingObserver swapped in,
// so a test can both drive its methods and assert which fields it observed.
func newRecordingEncryptor(t *testing.T, secret string) (*salsa20Impl, *observability.RecordingObserver) {
	t.Helper()

	ed, err := NewEncryptorDecryptor(tracingnoop.NewTracerProvider(), loggingnoop.NewLogger(), []byte(secret))
	must.NotNil(t, ed)
	must.NoError(t, err)

	e, ok := ed.(*salsa20Impl)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	e.o11y = obs

	return e, obs
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

		encrypted2, err := encryptor.Encrypt(ctx, expected)
		test.NoError(t, err)
		test.NotEq(t, "", encrypted2)

		test.EqOp(t, encrypted, encrypted2)

		actual, err := encryptor.Decrypt(ctx, encrypted)
		test.NoError(t, err)
		test.EqOp(t, expected, actual)
	})

	T.Run("decrypt observes content length", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		expected := t.Name()
		secret, err := random.GenerateHexEncodedString(ctx, 16)
		must.NoError(t, err)

		encryptor, obs := newRecordingEncryptor(t, secret)

		encrypted, err := encryptor.Encrypt(ctx, expected)
		test.NoError(t, err)

		actual, err := encryptor.Decrypt(ctx, encrypted)
		test.NoError(t, err)
		test.EqOp(t, expected, actual)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.LengthKey: len(encrypted),
		})
	})

	T.Run("decrypt with invalid base64 observes content length and records error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		secret, err := random.GenerateHexEncodedString(ctx, 16)
		must.NoError(t, err)

		encryptor, obs := newRecordingEncryptor(t, secret)

		const invalid = "not valid base64!!!"
		_, err = encryptor.Decrypt(ctx, invalid)
		must.Error(t, err)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.LengthKey: len(invalid),
		})
		must.SliceLen(t, 1, op.Errors)
	})
}
