package webauthn

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Sentinels. errors/http maps platform sentinels onto status codes and imports
// the packages it maps, so nothing here may import errors/http or errors/grpc.
var (
	// ErrSessionNotFound indicates no ceremony state is stored under the
	// challenge. It is what every unusable challenge reads as: never issued,
	// already consumed, or past its TTL.
	//
	// A caller should not distinguish those for the client's benefit. All three
	// mean the ceremony has to start again, and telling a caller which one it
	// was tells an attacker whether a challenge they guessed at ever existed.
	ErrSessionNotFound = platformerrors.New("webauthn ceremony session not found")

	// ErrSessionExpired indicates ceremony state was found and had passed its
	// TTL. It wraps ErrSessionNotFound, because a store that can tell the two
	// apart owes its callers the same answer as one that cannot — the
	// database store reads an expires_at column and knows; the cache store's
	// entry is simply gone.
	ErrSessionExpired = platformerrors.Wrap(ErrSessionNotFound, "webauthn ceremony session expired")

	// ErrChallengeRequired indicates a ceremony session with no challenge. It
	// wraps errors.ErrEmptyInputParameter, so a caller may check either.
	//
	// It is a rejection rather than a stored row under an empty key: the
	// challenge is the identity of the ceremony, and a store keyed on nothing
	// would hand the next empty-challenge lookup somebody else's session.
	ErrChallengeRequired = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty webauthn challenge")

	// ErrNilSession indicates Save was called without ceremony state. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilSession = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webauthn ceremony session")

	// ErrNonPositiveTTL indicates Save was given a TTL of zero or less, which
	// would store ceremony state that is unusable the instant it is written.
	// Zero cannot stand in for "no expiry" here: ceremony state that never
	// expires is a challenge that can be answered next year.
	ErrNonPositiveTTL = platformerrors.New("webauthn ceremony session ttl is not positive")

	// ErrNilStore indicates NewRelyingParty was called without a session
	// store. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	//
	// There is no default. An implicit in-memory store would pass every test
	// and fail intermittently in production the moment a second replica
	// existed, which is the failure this package is for.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webauthn session store")

	// ErrNilUser indicates a ceremony was begun or finished without a user. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilUser = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webauthn user")

	// ErrNilResponse indicates a ceremony was finished without the client's
	// response — a nil body reader. It wraps errors.ErrNilInputParameter, so a
	// caller may check either.
	//
	// The *http.Request entry points do not report it: the library's parsers
	// answer a nil request with their own bad-request error, and reporting two
	// different sentinels for the same missing thing would be a distinction
	// only this package could explain.
	ErrNilResponse = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webauthn ceremony response")

	// ErrNilHandler indicates a discoverable login was finished without a
	// handler to resolve the credential's owner. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilHandler = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webauthn discoverable user handler")
)
