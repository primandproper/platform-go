package noop

import (
	"context"

	"github.com/primandproper/platform-go/secrets"
)

var _ secrets.SecretSource = (*secretSource)(nil)

// secretSource returns empty string for all secrets.
type secretSource struct{}

// GetSecret returns empty string.
func (n *secretSource) GetSecret(ctx context.Context, name string) (string, error) {
	return "", nil
}

// Close is a no-op.
func (n *secretSource) Close() error {
	return nil
}

// NewSecretSource returns a SecretSource that returns empty strings.
func NewSecretSource() secrets.SecretSource {
	return &secretSource{}
}
