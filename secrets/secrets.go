package secrets

import (
	"context"

	"github.com/primandproper/platform-go/v4/errors"
)

// ErrSecretNotFound is returned when a requested secret does not exist, so a
// missing secret is distinguishable from one whose value is legitimately empty.
var ErrSecretNotFound = errors.New("secret not found")

// SecretSource provides access to secrets.
type SecretSource interface {
	GetSecret(ctx context.Context, name string) (string, error)
	Close() error
}
