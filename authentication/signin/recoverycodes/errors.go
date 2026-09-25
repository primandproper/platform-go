package recoverycodes

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The construction and argument refusals. Each wraps a platform sentinel, so the
// platform mappers answer them and this package declares no mappers of its own —
// the refusals a caller acts on are signin's, because they are the sign-in
// service's answers rather than this table's.
//
// The refusal a check or a spend actually produces is signin.ErrInvalidCredentials,
// returned from Verify and Consume rather than declared here: an unknown code, a
// spent one and one belonging to somebody else are one answer, and that answer is
// the one a wrong TOTP code gets at the same door.
var (
	// ErrNilConfig indicates NewSQLStore was called without a config.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil recovery code store config")

	// ErrNilDatabaseClient indicates NewSQLStore was called without a database
	// client.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the recovery code store")

	// ErrEmptyUserID indicates an operation naming nobody.
	ErrEmptyUserID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty user ID for a recovery code")

	// ErrEmptyCode indicates a check or a spend presenting nothing.
	//
	// It is this store's own refusal rather than signin.ErrInvalidCredentials,
	// because an empty argument is a caller that did not submit rather than a
	// guess that missed. signin never presents one — an empty second-factor
	// code is refused a step earlier as signin.ErrSecondFactorRequired — so this
	// is the backstop for a caller holding the store directly.
	ErrEmptyCode = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty recovery code")

	// ErrNonPositiveCount indicates a replacement asking for no codes.
	//
	// It is refused rather than read as "withdraw the set", because a caller
	// that meant to withdraw one wants DeleteForUser, and one that passed a zero
	// by mistake would otherwise leave a person holding nothing while believing
	// they had been handed a fresh set.
	ErrNonPositiveCount = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "non-positive recovery code count")
)
