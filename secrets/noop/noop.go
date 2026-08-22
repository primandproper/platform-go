// Package noop is the secrets.SecretSource that holds no secrets, and how it
// says so is the thing to know: GetSecret returns secrets.ErrSecretNotFound for
// every name it is ever asked.
//
// That sentinel exists precisely so a missing secret can be told apart from one
// whose value is legitimately empty, and answering an empty string with a nil
// error would collapse the distinction — a source that holds nothing would be
// reporting that every secret exists and is empty. A caller that branches on
// ErrSecretNotFound to fall back to a default takes that branch here, instead of
// configuring itself with empty credentials and discovering the problem later
// and further away, as an authentication failure against whatever those
// credentials were for.
//
// secrets/config builds it for the "noop" provider name, which has to be given.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/secrets"
)

var _ secrets.SecretSource = (*SecretSource)(nil)

// SecretSource holds no secrets.
type SecretSource struct{}

// GetSecret reports every name as absent, returning secrets.ErrSecretNotFound.
func (n *SecretSource) GetSecret(ctx context.Context, name string) (string, error) {
	return "", secrets.ErrSecretNotFound
}

// Close is a no-op.
func (n *SecretSource) Close() error {
	return nil
}

// NewSecretSource returns a SecretSource that reports every secret as absent.
func NewSecretSource() *SecretSource {
	return &SecretSource{}
}
