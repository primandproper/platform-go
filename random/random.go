package random

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"io"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// ErrNoRandomness is returned by a Generator that has no source of randomness to
// draw from, and is what random/noop answers with for every call.
//
// It exists because the alternative is an empty value and a nil error, which is
// indistinguishable at the call site from a successful draw. What this interface
// produces becomes two-factor secrets, salts, session and API tokens, and
// one-time codes, so a generator that yields the same empty value to every
// caller — and to every attacker comparing two of them — has to say so in the
// only channel a caller is obliged to check.
var ErrNoRandomness = platformerrors.New("generator has no source of randomness")

var (
	_ Generator = (*StandardGenerator)(nil)

	defaultGenerator = NewGenerator()
)

type (
	// Generator should generate random strings securely.
	Generator interface {
		GenerateHexEncodedString(ctx context.Context, length int) (string, error)
		GenerateBase32EncodedString(context.Context, int) (string, error)
		GenerateBase64EncodedString(context.Context, int) (string, error)
		GenerateRawBytes(context.Context, int) ([]byte, error)
	}

	// StandardGenerator is the one Generator implementation, over crypto/rand.
	// It is exported, and returned by NewGenerator, so a caller can depend on the
	// generator it built rather than on the Generator seam.
	StandardGenerator struct {
		o11y       observability.Observer
		randReader io.Reader
	}
)

// NewGenerator builds a new Generator.
func NewGenerator(opts ...Option) *StandardGenerator {
	o := newOptions(opts)

	return &StandardGenerator{
		o11y:       observability.NewObserver("random_generator", o.logger, o.tracerProvider),
		randReader: rand.Reader,
	}
}

// GenerateHexEncodedString generates a one-off value with an anonymous Generator.
func GenerateHexEncodedString(ctx context.Context, length int) (string, error) {
	return defaultGenerator.GenerateHexEncodedString(ctx, length)
}

// MustGenerateHexEncodedString generates a one-off value with an anonymous Generator.
func MustGenerateHexEncodedString(ctx context.Context, length int) string {
	x, err := defaultGenerator.GenerateHexEncodedString(ctx, length)
	if err != nil {
		panic(err)
	}

	return x
}

// GenerateBase32EncodedString generates a one-off value with an anonymous Generator.
func GenerateBase32EncodedString(ctx context.Context, length int) (string, error) {
	return defaultGenerator.GenerateBase32EncodedString(ctx, length)
}

// GenerateBase64EncodedString generates a one-off value with an anonymous Generator.
func GenerateBase64EncodedString(ctx context.Context, length int) (string, error) {
	return defaultGenerator.GenerateBase64EncodedString(ctx, length)
}

// GenerateRawBytes generates a one-off value with an anonymous Generator.
func GenerateRawBytes(ctx context.Context, length int) ([]byte, error) {
	return defaultGenerator.GenerateRawBytes(ctx, length)
}

// MustGenerateRawBytes generates a one-off value with an anonymous Generator, but does not return an error.
func MustGenerateRawBytes(ctx context.Context, length int) []byte {
	x, err := defaultGenerator.GenerateRawBytes(ctx, length)
	if err != nil {
		panic(err)
	}

	return x
}

// generateSecret fills a securely random byte array of a given length. It does not
// open its own span; the caller owns the span it passes in so that each public
// method produces a single span rather than nesting one per internal hop.
func (g *StandardGenerator) generateSecret(span tracing.Span, length int) ([]byte, error) {
	b := make([]byte, length)
	if _, err := io.ReadFull(g.randReader, b); err != nil {
		return nil, observability.PrepareError(err, span, "reading from secure random source")
	}

	return b, nil
}

// GenerateRawBytes generates a securely random byte array.
func (g *StandardGenerator) GenerateRawBytes(ctx context.Context, length int) ([]byte, error) {
	_, op := g.o11y.Begin(ctx, observability.WithValue(keys.LengthKey, length))
	defer op.End()

	return g.generateSecret(op.Span(), length)
}

// GenerateHexEncodedString generates a hex-encoded string of a securely random byte array of a given length.
func (g *StandardGenerator) GenerateHexEncodedString(ctx context.Context, length int) (string, error) {
	_, op := g.o11y.Begin(ctx, observability.WithValue(keys.LengthKey, length))
	defer op.End()

	b, err := g.generateSecret(op.Span(), length)
	if err != nil {
		return "", op.Error(err, "reading from secure random source")
	}

	return hex.EncodeToString(b), nil
}

// GenerateBase32EncodedString generates a base32-encoded string of a securely random byte array of a given length.
func (g *StandardGenerator) GenerateBase32EncodedString(ctx context.Context, length int) (string, error) {
	_, op := g.o11y.Begin(ctx, observability.WithValue(keys.LengthKey, length))
	defer op.End()

	b, err := g.generateSecret(op.Span(), length)
	if err != nil {
		return "", op.Error(err, "reading from secure random source")
	}

	return base32.StdEncoding.EncodeToString(b), nil
}

// GenerateBase64EncodedString generates a base64-encoded string of a securely random byte array of a given length.
func (g *StandardGenerator) GenerateBase64EncodedString(ctx context.Context, length int) (string, error) {
	_, op := g.o11y.Begin(ctx, observability.WithValue(keys.LengthKey, length))
	defer op.End()

	b, err := g.generateSecret(op.Span(), length)
	if err != nil {
		return "", op.Error(err, "reading from secure random source")
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}
