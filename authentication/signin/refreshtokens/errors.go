package refreshtokens

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The construction and argument refusals. Each wraps a platform sentinel, so the
// platform mappers answer them and this package declares no mappers of its own —
// the refusals a caller acts on are signin's, because they are the sign-in
// service's answers rather than this table's.
var (
	// ErrNilConfig indicates NewSQLStore was called without a config.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil refresh token store config")

	// ErrNilDatabaseClient indicates NewSQLStore was called without a database
	// client.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the refresh token store")

	// ErrNilRequest indicates Issue was called with no request on it.
	ErrNilRequest = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil refresh token request")

	// ErrEmptyFamilyID indicates a mint or a revocation naming no login.
	ErrEmptyFamilyID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty family ID for a refresh token")

	// ErrEmptySubjectID indicates a mint or a revocation naming nobody.
	ErrEmptySubjectID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty subject ID for a refresh token")

	// ErrEmptySecret indicates a redemption presenting nothing.
	ErrEmptySecret = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty refresh token secret")

	// ErrNonPositiveLifetime indicates a mint with no lifetime on it.
	//
	// It is refused rather than defaulted, because a refresh token with no
	// deadline is a sign-in that never ends and a default invented here would be
	// this package choosing how long everybody stays signed in. The lifetime is
	// signin's WithRefreshTokenTTL, which has one.
	ErrNonPositiveLifetime = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "non-positive refresh token lifetime")
)
