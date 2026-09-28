package phonecodes

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The sentinels this package returns.
var (
	// ErrCodeInvalid is how a caller reports every refusal a redemption can
	// produce: no code for the number, a spent code, a withdrawn one, an
	// expired one, one at its attempt limit, and a wrong code. Store.Redeem
	// answers a refusal as false rather than returning this, so that the
	// attempt it counted commits; the caller reports it after the commit.
	//
	// They are one answer so that a guesser learns nothing from which one it
	// got. Telling "that number has no code" from "wrong code" apart says which
	// numbers are waiting for one; telling "wrong code" from "too many attempts"
	// apart says when to move on to the next number. The span records which it
	// was, for the operator.
	ErrCodeInvalid = platformerrors.New("the code is not valid for that phone number")

	// ErrInvalidPhoneNumber indicates a number that is not in E.164: a plus
	// sign and up to fifteen digits, the first not zero.
	ErrInvalidPhoneNumber = platformerrors.New("phone number is not in E.164 form")

	// ErrEmptySubjectID indicates an issue, revocation, read or erasure naming
	// nobody. It is refused rather than treated as a wildcard.
	ErrEmptySubjectID = platformerrors.New("phone code subject is required")

	// ErrEmptyCode indicates a redemption presenting nothing.
	//
	// It is an argument refusal rather than a refused code, because an empty
	// submission is a form nobody filled in rather than a guess that missed, and
	// counting it as an attempt would let a double-clicked empty form spend a
	// person's tries.
	ErrEmptyCode = platformerrors.New("phone code is required")

	// ErrValueTooLong indicates a subject longer than MaxSubjectLength. It wraps
	// errors.ErrUnrecognizedInputValue, so the platform mapper answers it as a
	// bad request.
	ErrValueTooLong = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "phone code subject is too long")

	// ErrInvalidMaxAttempts indicates an attempt limit on an IssueRequest
	// that is negative or above the store's own, which a request may tighten
	// and never loosen. It wraps errors.ErrUnrecognizedInputValue.
	ErrInvalidMaxAttempts = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "phone code attempt limit is outside what the store allows")

	// ErrInvalidSetting indicates NewSQLStore was configured with a code
	// length, lifetime, attempt limit or retention it cannot honor — see the
	// wrapped message for which. It wraps errors.ErrUnrecognizedInputValue.
	//
	// It is refused rather than clamped: a store asked for four-digit codes
	// that silently issued six would be the configuration saying one thing and
	// the deployment doing another.
	ErrInvalidSetting = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "invalid phone code store setting")

	// ErrNilDatabaseClient indicates NewSQLStore was called without a database
	// client.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the phone code store")

	// ErrNilExecutor indicates a nil transaction or executor.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil phone code query executor")

	// ErrNilRequest indicates Issue was called with no request.
	ErrNilRequest = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil phone code issue request")
)
