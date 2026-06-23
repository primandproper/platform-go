package argon2_test

import (
	"testing"

	"github.com/primandproper/platform/authentication/argon2"
	loggingnoop "github.com/primandproper/platform/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

func BenchmarkArgon2Authenticator(b *testing.B) {
	a := argon2.ProvideArgon2Authenticator(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
	ctx := b.Context()
	const password = "Pa$$w0rdPa$$w0rdPa$$w0rdPa$$w0rd"

	b.Run("HashPassword", func(b *testing.B) {
		for b.Loop() {
			strSink, _ = a.HashPassword(ctx, password)
		}
	})

	b.Run("PasswordMatches", func(b *testing.B) {
		hash, err := a.HashPassword(ctx, password)
		must.NoError(b, err)
		for b.Loop() {
			boolSink, _ = a.PasswordMatches(ctx, hash, password)
		}
	})
}

var (
	strSink  string
	boolSink bool
)
