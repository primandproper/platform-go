// Package noop is the qrcodes.Builder that draws nothing. BuildQRCode returns
// an empty string and a nil error, where the real builder returns a
// "data:image/png;base64," URI.
//
// That empty string is what to plan for. It is a perfectly valid Go value and a
// useless image source, so a TOTP enrollment page templated with it renders a
// broken image rather than an error, and a user cannot complete two-factor
// setup because there is nothing to scan. The failure arrives in the browser,
// several layers from the decision that caused it.
//
// There is no qrcodes/config, so nothing can select this by name — a caller who
// wants it constructs it, which is the right shape for something that makes
// sense only in tests and in a build whose enrollment flow is unreachable.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/qrcodes"
)

var _ qrcodes.Builder = (*Builder)(nil)

// Builder is a no-op Builder.
type Builder struct{}

// NewBuilder returns a no-op Builder.
func NewBuilder() qrcodes.Builder {
	return &Builder{}
}

// BuildQRCode is a no-op.
func (*Builder) BuildQRCode(context.Context, string, string) (string, error) {
	return "", nil
}
