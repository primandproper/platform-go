package grants

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The sentinels this package returns. They live together because a caller
// deciding what to do next is choosing between them.
var (
	// ErrNilDatabaseClient indicates a nil database.Client handed to
	// NewSQLStore.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil grant database client")

	// ErrNilEncryptor indicates a nil encryptor handed to NewSQLStore. It is
	// required rather than defaulted: a store that fell back to storing tokens
	// as they are would be the plaintext table this package exists to prevent.
	ErrNilEncryptor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil grant token encryptor")

	// ErrNilExecutor indicates a nil executor. Every method on the Store runs
	// on one the caller supplies.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil grant query executor")

	// ErrNilConsent indicates a nil Consent handed to Store.Put.
	ErrNilConsent = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil grant consent")

	// ErrNilTokens indicates nil Tokens handed to Store.Refreshed.
	ErrNilTokens = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil grant tokens")

	// ErrNilGrant indicates a nil Grant handed to Refresh.
	ErrNilGrant = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil grant to refresh")

	// ErrNilExchanger indicates a nil Exchanger handed to Refresh.
	ErrNilExchanger = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil grant token exchanger")

	// ErrEmptySubject indicates a write or read that named no subject. It is
	// refused rather than treated as a wildcard: a read keyed on the empty
	// string answers with whatever a consumer happened to write with one.
	ErrEmptySubject = platformerrors.New("grant subject is required")

	// ErrEmptyProvider indicates a write or read that named no provider.
	ErrEmptyProvider = platformerrors.New("grant provider is required")

	// ErrEmptyAccessToken indicates a consent, or tokens handed to
	// Store.Refreshed, carrying no access token. A grant is its access token;
	// one without is not a grant.
	ErrEmptyAccessToken = platformerrors.New("grant access token is required")

	// ErrInvalidGrantedScope indicates a granted scope that is not an RFC 6749
	// scope token — empty, or containing a space, a quote or a backslash — and
	// so would not read back as the value that was written.
	ErrInvalidGrantedScope = platformerrors.New("granted scope is not an OAuth2 scope token")

	// ErrValueTooLong indicates a value longer than the column that stores it —
	// see MaxSubjectLength and the bounds beside it, and the wrapped message
	// for which value it was. It wraps errors.ErrUnrecognizedInputValue, so the
	// platform mapper answers it as a bad request.
	ErrValueTooLong = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "grant value is too long")

	// ErrUnknownRevocationReason indicates a revocation naming a reason this
	// package does not record. It wraps errors.ErrUnrecognizedInputValue.
	ErrUnknownRevocationReason = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "unknown grant revocation reason")

	// ErrGrantNotFound indicates no live grant where the call looked. A grant
	// in another scope reads as absent, which is what it is from here, and so
	// does a revoked one: a revoked grant authorizes nothing.
	ErrGrantNotFound = platformerrors.New("grant not found")

	// ErrStaleRefresh indicates a refresh whose caller refreshed from an access
	// token the row no longer holds: somebody else refreshed the grant first,
	// or a consent replaced it.
	//
	// It is the loser's answer in the compare-and-set, and what it asks of the
	// caller is to read the grant again. The winner's tokens are the stored
	// ones; the loser's may be just as valid at the provider, but writing them
	// over the winner's would store whichever answer arrived last rather than
	// the one every other replica is already using — and on a provider that
	// rotates refresh tokens, would store one the provider has already
	// invalidated.
	ErrStaleRefresh = platformerrors.New("grant was refreshed from a token it no longer holds")

	// ErrNoRefreshToken indicates a Refresh of a grant that holds no refresh
	// token. The provider issued none at consent, so there is nothing to
	// exchange and the subject has to consent again when the access token
	// lapses.
	ErrNoRefreshToken = platformerrors.New("grant holds no refresh token")

	// ErrProviderRevoked indicates a refresh the provider refused with
	// invalid_grant: the subject revoked access on the provider's side, the
	// refresh token expired, or the provider rotated it out from under this
	// grant. Retrying will not help. The caller records it with Store.Revoke
	// and RevokedByProvider, which is what stops whatever retries refreshes
	// from retrying this one forever.
	ErrProviderRevoked = platformerrors.New("the provider refused the grant's refresh token")

	// ErrProviderReturnedNoAccessToken indicates a refresh the provider answered
	// with success and no access token. It is the provider's fault rather than
	// the caller's, which is why it is not ErrEmptyAccessToken: that one is a
	// consent or a Store.Refreshed the caller assembled without a token, and is
	// answered as a bad request. Nothing is stored and nothing is revoked — the
	// grant still holds what it held, and whether a retry fares better is the
	// provider's to say.
	ErrProviderReturnedNoAccessToken = platformerrors.New("the provider answered a grant refresh with no access token")
)
