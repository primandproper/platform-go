// Package noop is the random.Generator that has nothing to draw from: every
// method returns random.ErrNoRandomness and no value.
//
// Read that against what the real generator is for. This interface backs
// two-factor secrets, salts, session and API tokens, and one-time codes, so the
// one thing this generator must not do is answer an empty value with a nil
// error — that is indistinguishable at the call site from a successful draw, and
// a system wired with it would issue the same empty token to every caller while
// comparing two of those tokens succeeded. Failing every call instead keeps it
// from being used by accident: whatever was going to hold the token gets an
// error where it expected a secret.
//
// It exists for tests that need a Generator to satisfy a signature and never
// read what it produced. There is no random/config, so nothing selects it from
// configuration; a caller who wants it names it in code, which is the only
// place a decision like this one should be visible.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/random"
)

var _ random.Generator = (*Generator)(nil)

// Generator is a no-op Generator.
type Generator struct{}

// NewGenerator returns a no-op Generator.
func NewGenerator() random.Generator {
	return &Generator{}
}

// GenerateHexEncodedString returns random.ErrNoRandomness.
func (*Generator) GenerateHexEncodedString(context.Context, int) (string, error) {
	return "", random.ErrNoRandomness
}

// GenerateBase32EncodedString returns random.ErrNoRandomness.
func (*Generator) GenerateBase32EncodedString(context.Context, int) (string, error) {
	return "", random.ErrNoRandomness
}

// GenerateBase64EncodedString returns random.ErrNoRandomness.
func (*Generator) GenerateBase64EncodedString(context.Context, int) (string, error) {
	return "", random.ErrNoRandomness
}

// GenerateRawBytes returns random.ErrNoRandomness.
func (*Generator) GenerateRawBytes(context.Context, int) ([]byte, error) {
	return nil, random.ErrNoRandomness
}
