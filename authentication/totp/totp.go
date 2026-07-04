// Package totp provides a TOTP (RFC 6238) second-factor verifier. It is
// intentionally decoupled from authentication.Authenticator so that password
// verification and second-factor verification can evolve independently.
package totp

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/observability"
	"github.com/primandproper/platform-go/v3/observability/tracing"

	"github.com/pquerna/otp/totp"
)

const serviceName = "totp"

var (
	// ErrInvalidCode indicates the provided TOTP code did not validate against the secret.
	ErrInvalidCode = platformerrors.New("invalid TOTP code")
	// ErrCodeRequired indicates TOTP is enabled but no code was provided.
	ErrCodeRequired = platformerrors.New("TOTP code required but not provided")
)

// Verifier verifies a TOTP code against a shared secret.
type Verifier interface {
	// Verify returns nil if code is valid for secret. It returns ErrCodeRequired
	// if code is empty, and ErrInvalidCode if the code does not validate.
	Verify(ctx context.Context, secret, code string) error
}

type verifier struct {
	o11y observability.Observer
}

// NewVerifier returns a Verifier backed by github.com/pquerna/otp.
func NewVerifier(tracerProvider tracing.TracerProvider) Verifier {
	return &verifier{
		o11y: observability.NewObserver(serviceName, nil, tracerProvider),
	}
}

// Verify implements Verifier.
func (v *verifier) Verify(ctx context.Context, secret, code string) error {
	_, op := v.o11y.Begin(ctx)
	defer op.End()

	if code == "" {
		return ErrCodeRequired
	}

	if !totp.Validate(code, secret) {
		return ErrInvalidCode
	}

	return nil
}
