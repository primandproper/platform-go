package noop

import (
	"context"

	"github.com/primandproper/platform-go/qrcodes"
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
