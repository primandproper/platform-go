package salsa20

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"
	"github.com/primandproper/platform-go/random"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

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
}
