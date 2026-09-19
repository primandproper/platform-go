package passwordreset

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

var (
	// ErrTokenNotFound indicates a secret that matches no row in the scope it
	// was presented in — never issued, already swept, or issued to a different
	// tenant, which are the same answer from here.
	ErrTokenNotFound = platformerrors.New("password reset token not found")

	// ErrTokenExpired indicates a token past its deadline. It is distinguished
	// from ErrTokenNotFound deliberately: an attacker learns this only by
	// already holding the token, and a user holding a day-old link is owed the
	// difference between "that link expired" and "that link is not a link".
	ErrTokenExpired = platformerrors.New("password reset token has expired")

	// ErrTokenRedeemed indicates a token that has already been spent, including
	// one spent by a concurrent redemption a fraction of a second earlier. It
	// is the answer exactly one of two racing callers receives, and the reason
	// single use is a property of the store rather than of whoever called it.
	ErrTokenRedeemed = platformerrors.New("password reset token has already been redeemed")

	// ErrEmptySecret indicates a verification or redemption that named no
	// token. It wraps errors.ErrEmptyInputParameter, so a caller may check
	// either.
	ErrEmptySecret = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "no password reset token provided")

	// ErrEmptyUserID indicates an issuance that named no principal. It wraps
	// errors.ErrEmptyInputParameter, so a caller may check either.
	ErrEmptyUserID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "no user identifier provided")

	// ErrNonPositiveLifetime indicates an issuance whose token would already
	// have expired. It is refused rather than clamped: a zero TTL is a caller
	// reading an unset configuration field, and issuing a dead link for it
	// produces a reset flow that fails at the last step for reasons nothing
	// explains.
	ErrNonPositiveLifetime = platformerrors.New("password reset token lifetime must be positive")

	// ErrNilDatabaseClient indicates NewSQLStore was called without a database
	// client. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil password reset database client")

	// ErrNilConfig indicates NewSQLStore was called without a config. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil password reset store config")
)

// The sentinels the flow over the store adds. All six are arguments Service
// refuses rather than outcomes a person meets — the three outcomes a person
// meets are the three above, and the flow returns them unchanged.
var (
	// ErrNilStore indicates NewService was called without a Store. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter,
		"nil token store for the password reset service")

	// ErrNilDirectory indicates NewService was called without a Directory. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDirectory = platformerrors.Wrap(platformerrors.ErrNilInputParameter,
		"nil directory for the password reset service")

	// ErrNilAuthenticator indicates NewService was called without an
	// authentication.Authenticator. There is no default and there will not be
	// one: which engine hashes a deployment's passwords is not a choice a
	// library makes on its behalf. It wraps errors.ErrNilInputParameter, so a
	// caller may check either.
	ErrNilAuthenticator = platformerrors.Wrap(platformerrors.ErrNilInputParameter,
		"nil authenticator for the password reset service")

	// ErrNilMailer indicates NewService was called without a Mailer. A reset
	// flow that cannot deliver the secret is a flow nobody can complete, so it
	// is refused at construction rather than at the first request. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilMailer = platformerrors.Wrap(platformerrors.ErrNilInputParameter,
		"nil mailer for the password reset service")

	// ErrEmptyEmailAddress indicates a reset request that named nobody. It is
	// the caller's own bug rather than an unknown address — which is answered
	// with success, and see Service.Request for why — and it wraps
	// errors.ErrEmptyInputParameter, so a caller may check either.
	ErrEmptyEmailAddress = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter,
		"no email address provided")

	// ErrEmptyNewPassword indicates a redemption that carried no replacement
	// password. It is refused before the token is spent, so a client that
	// submitted an empty form still holds its link. It wraps
	// errors.ErrEmptyInputParameter, so a caller may check either.
	ErrEmptyNewPassword = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter,
		"no replacement password provided")
)
