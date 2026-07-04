package salsa20

import (
	"strings"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

func BenchmarkEncryptorDecryptor(b *testing.B) {
	ed, err := NewEncryptorDecryptor(tracingnoop.NewTracerProvider(), loggingnoop.NewLogger(), []byte("0123456789abcdef0123456789abcdef"))
	must.NoError(b, err)

	ctx := b.Context()
	plaintext := strings.Repeat("x", 256)

	b.Run("Encrypt", func(b *testing.B) {
		for b.Loop() {
			strSink, _ = ed.Encrypt(ctx, plaintext)
		}
	})

	b.Run("Decrypt", func(b *testing.B) {
		ciphertext, encErr := ed.Encrypt(ctx, plaintext)
		must.NoError(b, encErr)
		for b.Loop() {
			strSink, _ = ed.Decrypt(ctx, ciphertext)
		}
	})
}

var strSink string
