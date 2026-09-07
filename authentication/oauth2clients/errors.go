package oauth2clients

import (
	platformerrors "github.com/primandproper/platform-go/v14/errors"
)

// The wiring failures. Each wraps a platform sentinel, so errors/http and
// errors/grpc already answer for them and this package's own mappers say
// nothing about them.
var (
	// ErrNilDatabaseClient is a store or service built without a handle to open
	// a transaction with.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client")

	// ErrNilStore is a service built over no store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 client store")

	// ErrNilService is a transport built over no service.
	ErrNilService = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 client service")

	// ErrNilExecutor is a read handed no executor. Reads take the wider type so
	// that one method serves a caller holding Client.Reader() and a caller
	// inside a transaction; neither is satisfied by nil.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 client query executor")

	// ErrNilTransaction is a write handed no transaction. Every write here takes
	// a database.Tx, which is producible only by database.RunInTransaction, so
	// the argument is the caller's proof that the audit entry and the outbox row
	// beside this one land or roll back together.
	ErrNilTransaction = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 client transaction")

	// ErrNilClient is a write handed no registration.
	ErrNilClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 client")

	// ErrNilInput is a create or update handed no input.
	ErrNilInput = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 client input")

	// ErrEmptyID is a read or write naming no row.
	ErrEmptyID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty oauth2 client id")

	// ErrEmptyClientID is a lookup naming no client identifier.
	ErrEmptyClientID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty oauth2 client identifier")

	// ErrEmptyUserID is a self-service call that named no person.
	ErrEmptyUserID = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty oauth2 client owner id")
)

// The refusals a caller can act on. Each is mapped by this package's own
// HTTPMapper and GRPCMapper — see errormappers.go.
var (
	// ErrEmptyName is a registration with no name.
	//
	// The name is what the consent form shows the person being asked to
	// authorize this client, so a registration without one asks somebody to
	// grant access to nothing they can identify.
	ErrEmptyName = platformerrors.New("oauth2 client name is required")

	// ErrNoRedirectURIs is a registration naming no redirect URI.
	//
	// At least one is required, and the reason is that the authorization server
	// matches redirect_uri byte for byte against this list: a client registered
	// without one can authenticate at the token endpoint and still never
	// complete an authorization request. Refusing at registration is the only
	// place that failure is legible; later it is a client that "does not work".
	ErrNoRedirectURIs = platformerrors.New("oauth2 client requires at least one redirect URI")

	// ErrInvalidRedirectURI is a redirect URI the authorization server would not
	// accept. It is checked here, at registration, against the same
	// oauth2server.ValidateRedirectURI the server applies at /authorize, so the
	// two cannot disagree about what is registrable.
	ErrInvalidRedirectURI = platformerrors.New("oauth2 client redirect URI is not valid")

	// ErrClientNotFound is a registration that is not there, or is archived, or
	// belongs to another registry.
	//
	// The three are one answer on purpose. Telling "no such client" from "not
	// yours" makes the read an enumeration oracle over every other tenant's
	// registrations.
	ErrClientNotFound = platformerrors.New("oauth2 client not found")

	// ErrClientIDTaken is a minted client identifier that was already in use.
	//
	// It is not a caller error and there is nothing for them to correct — the
	// identifier is minted here from crypto/rand — so it is the one refusal in
	// this list that means "try again" rather than "send something else". It
	// exists as a sentinel because the alternative is the driver's unique
	// violation reaching a handler as a 500.
	ErrClientIDTaken = platformerrors.New("oauth2 client identifier already in use")

	// ErrScopeMismatch is a registration whose own scope disagrees with the
	// scope the call named.
	//
	// It is refused rather than corrected, which is the reading comments takes
	// of the same situation: a scope derived from a field the caller assembled
	// somewhere else is exactly the derivation the column rule exists to rule
	// out. A registration naming no scope adopts the argument's.
	ErrScopeMismatch = platformerrors.New("oauth2 client scope does not match the scope named")

	// ErrOwnerMismatch is a self-service call against a registration the caller
	// does not own.
	//
	// It is what makes the self-service half safe without a permission: the
	// grant is "you may manage your own", and this is the check that the row in
	// front of it is one of theirs.
	//
	// It maps to the same status and the same words as [ErrClientNotFound],
	// deliberately, and exists as a separate sentinel for the log rather than
	// for the wire: a caller who cannot tell "not yours" from "not there" cannot
	// walk the registry's identifiers, and an operator reading this process's
	// logs can still tell which of the two happened. See the mappers, and
	// oauth2clients/grpc's Server.own.
	ErrOwnerMismatch = platformerrors.New("oauth2 client belongs to somebody else")
)

// The two refusals an authorization request meets, and the only two sentinels
// here whose wording is written for the person reading it — which is why they
// are this package's ClientSafeSentinels.
var (
	// ErrClientScopeMismatch is a registration in one registry being used to
	// authorize a subject in another.
	//
	// It is the cross-tenant guarantee, and it is the whole reason the
	// authorization server's by-client_id lookup can afford to take no scope:
	// resolving a registration grants nobody anything, and this is where a
	// resolved one becomes a permitted one. See [Client.Admits].
	ErrClientScopeMismatch = platformerrors.New("this application is not registered for your organization")

	// ErrClientOwnerMismatch is a registration owned by one person being used to
	// authorize somebody else.
	//
	// A personal API credential that could sign another person in is an account
	// takeover with a client_id in front of it, so the owner is checked at the
	// same point and in the same method as the registry.
	ErrClientOwnerMismatch = platformerrors.New("this application belongs to another user")
)

// ErrSecretGeneration is a client secret crypto/rand would not produce.
//
// It is deliberately unmapped: there is nothing a caller did wrong and nothing
// they can change, so a 500 is the honest answer and the useful signal is in
// this process's own logs.
var ErrSecretGeneration = platformerrors.New("could not generate an oauth2 client secret")
