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
