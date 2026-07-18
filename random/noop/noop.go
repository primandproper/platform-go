package noop

import (
	"context"

	"github.com/primandproper/platform-go/v5/random"
)

var _ random.Generator = (*Generator)(nil)

// Generator is a no-op Generator.
type Generator struct{}

// NewGenerator returns a no-op Generator.
func NewGenerator() random.Generator {
	return &Generator{}
}

// GenerateHexEncodedString is a no-op.
func (*Generator) GenerateHexEncodedString(context.Context, int) (string, error) {
	return "", nil
}

// GenerateBase32EncodedString is a no-op.
func (*Generator) GenerateBase32EncodedString(context.Context, int) (string, error) {
	return "", nil
}

// GenerateBase64EncodedString is a no-op.
func (*Generator) GenerateBase64EncodedString(context.Context, int) (string, error) {
	return "", nil
}

// GenerateRawBytes is a no-op.
func (*Generator) GenerateRawBytes(context.Context, int) ([]byte, error) {
	return []byte{}, nil
}
