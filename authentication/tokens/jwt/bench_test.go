package jwt

import (
	"testing"
	"time"

	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

func BenchmarkJWTSigner(b *testing.B) {
	s, err := NewJWTSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-bench", "bench", []byte("HEREISA32CHARSECRETWHICHISMADEUP"))
	must.NoError(b, err)

	ctx := b.Context()
	claims := map[string]any{"account_id": "account_123"}

	b.Run("IssueToken", func(b *testing.B) {
		for b.Loop() {
			strSink, _, _ = s.IssueToken(ctx, "user_123", 10*time.Minute, claims)
		}
	})

	b.Run("ParseToken", func(b *testing.B) {
		tok, _, issErr := s.IssueToken(ctx, "user_123", 10*time.Minute, claims)
		must.NoError(b, issErr)
		for b.Loop() {
			_, _ = s.ParseToken(ctx, tok)
		}
	})
}

var strSink string
