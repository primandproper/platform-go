// Package totp provides a TOTP (RFC 6238) second-factor verifier. It is
// intentionally decoupled from authentication.Authenticator so that password
// verification and second-factor verification can evolve independently.
package totp

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"

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

var _ Verifier = (*TOTPVerifier)(nil)

// TOTPVerifier is the github.com/pquerna/otp-backed Verifier. It is exported,
// and returned by NewVerifier, so a caller can depend on the verifier it built
// rather than on the Verifier seam.
type TOTPVerifier struct {
	o11y observability.Observer
}

// NewVerifier returns a Verifier backed by github.com/pquerna/otp.
func NewVerifier(opts ...Option) *TOTPVerifier {
	o := newOptions(opts)

	return &TOTPVerifier{
		o11y: observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}
}

// Verify implements Verifier.
//
// A rejection is recorded on the span and logged, because a second factor that
// fails is the interesting half of this package: a password that worked
// followed by a code that did not is what a stuffing run looks like from the
// inside, and the sentinel alone leaves that to whoever remembered to log it.
//
// Neither the code nor the secret reaches telemetry. What is recorded is that a
// verification failed and why, which is what a deployment can alert on without
// putting a live second factor in a log aggregator.
func (v *TOTPVerifier) Verify(ctx context.Context, secret, code string) error {
	_, op := v.o11y.Begin(ctx)
	defer op.End()

	if code == "" {
		return op.Error(ErrCodeRequired, "verifying TOTP code")
	}

	if !totp.Validate(code, secret) {
		return op.Error(ErrInvalidCode, "verifying TOTP code")
	}

	return nil
}
