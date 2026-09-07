package totp

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/observability"

	"github.com/pquerna/otp/totp"
)

const generatorName = "totp_generator"

var (
	// ErrEmptyIssuer indicates a Generate call that named no issuer. It wraps
	// errors.ErrEmptyInputParameter, so a caller may check either.
	//
	// The issuer is what an authenticator app labels the entry with, so a blank
	// one produces a code nobody can tell apart from the six other codes on the
	// same screen. That is a support ticket rather than a failure, which is why
	// it is refused here instead of defaulted to something this package invents.
	ErrEmptyIssuer = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty TOTP issuer")

	// ErrEmptyAccountName indicates a Generate call that named no account. It
	// wraps errors.ErrEmptyInputParameter, so a caller may check either.
	ErrEmptyAccountName = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty TOTP account name")
)

// Enrollment is a freshly generated secret and the URI that delivers it.
//
// Both describe the same secret, and both are needed: the URI is what a client
// renders as a QR code for a camera, and the secret is what a person types when
// the camera will not focus. An enrollment flow that ships only one of them has
// a population it cannot enroll.
//
// Neither field belongs in a log line or a response the subject did not ask
// for. This is the one moment a TOTP secret is legitimately in flight, and it
// is in flight to exactly one person.
type Enrollment struct {
	_ struct{} `json:"-"`

	// Secret is the base32 shared secret, in the form the identity package's
	// User.TwoFactorSecret column stores.
	Secret string `json:"secret"`

	// URI is the otpauth:// URI the secret and its labels encode to, which is
	// what a QR code carries. Render it with this module's qrcodes package, or
	// hand it to a client that will.
	URI string `json:"uri"`
}

// Generator mints TOTP secrets.
//
// It is a seam of its own rather than a method on Verifier because the two are
// used by different halves of an enrollment: something generates a secret once,
// and something verifies codes against it forever after. A caller that only
// checks codes — a sign-in path, a middleware — depends on Verifier and cannot
// mint anything, which is the same narrowing this package's split into two
// interfaces already buys.
type Generator interface {
	// Generate mints a secret for accountName, labeled with issuer.
	//
	// The issuer is the application's name as it should appear in an
	// authenticator app, and the account name is how the person is identified
	// within it — a username or an email address. Both are required.
	Generate(ctx context.Context, issuer, accountName string) (*Enrollment, error)
}

var _ Generator = (*TOTPGenerator)(nil)

// TOTPGenerator is the github.com/pquerna/otp-backed Generator. It is exported,
// and returned by NewGenerator, so a caller can depend on the generator it built
// rather than on the Generator seam.
type TOTPGenerator struct {
	o11y observability.Observer
}

// NewGenerator returns a Generator backed by github.com/pquerna/otp.
func NewGenerator(opts ...Option) *TOTPGenerator {
	o := newOptions(opts)

	return &TOTPGenerator{
		o11y: observability.NewObserver(generatorName, o.logger, o.tracerProvider),
	}
}

// Generate implements Generator.
//
// Neither the secret nor the URI reaches telemetry, for the reason Enrollment
// gives. What is recorded is that a secret was minted and for which issuer,
// which is what a deployment can alert on — a spike in enrollments is a real
// signal — without putting a live second factor in a log aggregator.
func (g *TOTPGenerator) Generate(ctx context.Context, issuer, accountName string) (*Enrollment, error) {
	_, op := g.o11y.Begin(ctx, observability.WithValue("totp.issuer", issuer))
	defer op.End()

	if issuer == "" {
		return nil, op.Error(ErrEmptyIssuer, "generating TOTP secret")
	}

	if accountName == "" {
		return nil, op.Error(ErrEmptyAccountName, "generating TOTP secret")
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: accountName,
	})
	if err != nil {
		return nil, op.Error(err, "generating TOTP secret")
	}

	return &Enrollment{Secret: key.Secret(), URI: key.URL()}, nil
}
