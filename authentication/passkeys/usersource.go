package passkeys

import (
	"context"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// AuthenticatorFlags is the pair of backup flags a ceremony reported.
//
// They are carried as an argument rather than stored as columns, and the reason
// is a bug every from-scratch implementation ships once. go-webauthn refuses an
// assertion whose BackupEligible flag disagrees with the one on the credential
// it is verifying; a passkey synced through a platform keychain reports flags
// that differ from whatever the registration saw, because syncing is exactly the
// thing those flags describe and it happens after the fact. A relying party that
// replays stored flags therefore rejects perfectly valid logins, and the error it
// raises talks about flag inconsistency rather than about the row.
//
// So the flags come off the ceremony in hand. A caller that has parsed an
// assertion reads them from its authenticator data:
//
//	flags := passkeys.AuthenticatorFlags{
//		BackupEligible: parsed.Response.AuthenticatorData.Flags.HasBackupEligible(),
//		BackupState:    parsed.Response.AuthenticatorData.Flags.HasBackupState(),
//	}
//
// A registration, or the beginning half of a login, has no assertion to read and
// passes the zero value: nothing compares the flags on those paths.
type AuthenticatorFlags struct {
	// BackupEligible is whether the credential can be backed up or synced
	// between devices.
	BackupEligible bool
	// BackupState is whether it currently is.
	BackupState bool
}

// UserResolver answers the one question this package cannot answer for itself:
// which of this application's users a WebAuthn user handle names.
//
// A handle is an opaque byte string the relying party chose at registration and
// the authenticator returns at login. What it maps to is the consumer's
// directory's business — this module's identity package for some deployments,
// something else entirely for others — and a package here that guessed would be a
// package that assumed identity.
//
// It returns ErrCredentialNotFound's peer from the caller's own vocabulary for a
// handle that names nobody; UserSource passes whatever it gets back unchanged, so
// a consumer's "no such user" reaches its own caller as itself.
type UserResolver func(ctx context.Context, handle []byte) (UserIdentity, error)

// UserIdentity is what a resolver answers with: who the handle names, in the two
// forms a ceremony needs.
//
// The handle is not among them. It is the argument the resolver was called with,
// and a second copy on the way back would be a field free to disagree with it.
type UserIdentity struct {
	// UserID is the consumer's own user id — the value stored in
	// Credential.BelongsToUser, and what this package reads a user's passkeys
	// by.
	UserID string
	// Name is the account's human-palatable name, for display during a
	// ceremony.
	Name string
	// DisplayName is the account's display name, likewise.
	DisplayName string
}

// UserSource assembles the webauthn.User a ceremony takes, from stored
// credentials and a caller-supplied answer to who a handle belongs to.
//
// It is the seam that makes this package usable without it knowing what a user
// is. Everything on the other side of that seam — the rows, their order, the
// conversion into protocol credentials, the flags — is this package's; the
// directory lookup is the consumer's, and arrives as a function.
type UserSource struct {
	store   Store
	resolve UserResolver
}

// NewUserSource builds a UserSource over a store and a handle resolver.
//
// Both are required and neither has a default worth having. A nil store is
// ErrNilStore; a nil resolver is ErrNilResolver rather than a source that
// resolves every handle to nobody, which would be a relying party that refuses
// every login while looking configured.
func NewUserSource(store Store, resolve UserResolver) (*UserSource, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if resolve == nil {
		return nil, ErrNilResolver
	}

	return &UserSource{store: store, resolve: resolve}, nil
}

// User assembles the webauthn.User behind one handle, within one scope.
//
// The credentials are the scope's live rows for the user the resolver named, in
// the order they were enrolled, each rendered under flags — see
// AuthenticatorFlags for why those are an argument.
//
// A handle the resolver does not recognize is whatever error the resolver
// returned, passed through unchanged: this package has no idea which of the
// consumer's failures "no such user" is, and wrapping it in one of its own
// sentinels would hide the answer the consumer already gave.
func (u *UserSource) User(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	handle []byte,
	flags AuthenticatorFlags,
) (webauthn.User, error) {
	if len(handle) == 0 {
		return nil, ErrEmptyHandle
	}

	identity, err := u.resolve(ctx, handle)
	if err != nil {
		return nil, err
	}

	if identity.UserID == "" {
		return nil, platformerrors.Wrap(ErrEmptyUserID, "resolving a webauthn user handle")
	}

	stored, err := u.store.GetCredentialsForUser(ctx, q, scope, identity.UserID)
	if err != nil {
		return nil, err
	}

	credentials := make([]webauthn.Credential, 0, len(stored))
	for _, credential := range stored {
		credentials = append(credentials, credential.WebAuthnCredential(flags))
	}

	return &user{
		handle:      handle,
		identity:    identity,
		credentials: credentials,
	}, nil
}

// DiscoverableUserHandler binds a scope, an executor and a ceremony's flags into
// the callback go-webauthn invokes during a usernameless login.
//
// The library's callback takes only the raw credential ID and the user handle, so
// everything else a lookup needs has to be bound before the ceremony starts —
// which is also why the flags are bound here rather than read inside. A caller
// finishing a discoverable login has already parsed the assertion, so it has them.
//
// The raw credential ID is deliberately unused. The handle is what names the
// user, and a lookup keyed on the credential would answer with the user that
// credential belongs to rather than the one the authenticator says it is
// asserting for — which is the same answer in every honest case and a
// confused-deputy in the one case that matters.
func (u *UserSource) DiscoverableUserHandler(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	flags AuthenticatorFlags,
) webauthn.DiscoverableUserHandler {
	return func(_, handle []byte) (webauthn.User, error) {
		return u.User(ctx, q, scope, handle, flags)
	}
}

// user is the webauthn.User this package assembles. It is unexported because it
// is an answer rather than a type a consumer builds: everything on it came from
// the resolver or from the store.
type user struct {
	identity    UserIdentity
	handle      []byte
	credentials []webauthn.Credential
}

var _ webauthn.User = (*user)(nil)

// WebAuthnID returns the handle the ceremony was asked about, unchanged. Every
// authentication decision is made against it rather than against either name.
func (u *user) WebAuthnID() []byte { return u.handle }

// WebAuthnName returns the account's human-palatable name.
func (u *user) WebAuthnName() string { return u.identity.Name }

// WebAuthnDisplayName returns the account's display name.
func (u *user) WebAuthnDisplayName() string { return u.identity.DisplayName }

// WebAuthnCredentials returns the user's live passkeys, oldest first.
func (u *user) WebAuthnCredentials() []webauthn.Credential { return u.credentials }
