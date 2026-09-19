package passkeys_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passkeys"
	passkeysmock "github.com/primandproper/platform-go/v14/authentication/passkeys/mock"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testScope is the tenant these cases assemble users in.
var testScope = tenancy.Of("tenant_1")

// errNoSuchUser stands in for whatever a consumer's directory says about a
// handle it does not recognize. It is deliberately not one of this package's
// sentinels: the point of the cases below is that it comes back unchanged.
var errNoSuchUser = platformerrors.New("no such user in this directory")

// storeReturning is a Store that answers every read with the same credentials.
func storeReturning(credentials []*passkeys.Credential) *passkeysmock.StoreMock {
	return &passkeysmock.StoreMock{
		GetCredentialsForUserFunc: func(
			_ context.Context, _ database.SQLQueryExecutor, _ tenancy.Scope, _ string,
		) ([]*passkeys.Credential, error) {
			return credentials, nil
		},
	}
}

// resolverFor answers one handle and nothing else.
func resolverFor(handle []byte, identity passkeys.UserIdentity) passkeys.UserResolver {
	return func(_ context.Context, asked []byte) (passkeys.UserIdentity, error) {
		if !bytes.Equal(asked, handle) {
			return passkeys.UserIdentity{}, errNoSuchUser
		}

		return identity, nil
	}
}

func TestNewUserSource(T *testing.T) {
	T.Parallel()

	T.Run("both halves are required", func(t *testing.T) {
		t.Parallel()

		// A nil resolver would be a relying party that refuses every login while
		// looking configured, which is the failure this refusal exists to make
		// loud.
		_, noStore := passkeys.NewUserSource(nil, func(context.Context, []byte) (passkeys.UserIdentity, error) {
			return passkeys.UserIdentity{}, nil
		})
		must.ErrorIs(t, noStore, passkeys.ErrNilStore)

		_, noResolver := passkeys.NewUserSource(&passkeysmock.StoreMock{}, nil)
		must.ErrorIs(t, noResolver, passkeys.ErrNilResolver)
	})
}

func TestUserSource_User(T *testing.T) {
	T.Parallel()

	handle := []byte("opaque-handle")
	identity := passkeys.UserIdentity{UserID: "user_1", Name: "grace", DisplayName: "Grace Hopper"}

	T.Run("the assembled user carries the handle and the stored credentials", func(t *testing.T) {
		t.Parallel()

		stored := []*passkeys.Credential{
			{BelongsToUser: "user_1", CredentialID: []byte{0x01}, PublicKey: []byte{0x02}, SignCount: 3},
			{BelongsToUser: "user_1", CredentialID: []byte{0x03}, PublicKey: []byte{0x04}},
		}

		source, err := passkeys.NewUserSource(storeReturning(stored), resolverFor(handle, identity))
		must.NoError(t, err)

		user, err := source.User(t.Context(), nil, testScope, handle, passkeys.AuthenticatorFlags{})
		must.NoError(t, err)

		// The handle the ceremony asked about comes back unchanged: every
		// authentication decision is made against it rather than against either
		// name.
		test.Eq(t, handle, user.WebAuthnID())
		test.EqOp(t, "grace", user.WebAuthnName())
		test.EqOp(t, "Grace Hopper", user.WebAuthnDisplayName())

		credentials := user.WebAuthnCredentials()
		must.SliceLen(t, 2, credentials)
		test.Eq(t, []byte{0x01}, credentials[0].ID)
		test.EqOp(t, uint32(3), credentials[0].Authenticator.SignCount)
	})

	T.Run("the ceremony's flags reach every credential", func(t *testing.T) {
		t.Parallel()

		stored := []*passkeys.Credential{
			{BelongsToUser: "user_1", CredentialID: []byte{0x01}, PublicKey: []byte{0x02}},
		}

		source, err := passkeys.NewUserSource(storeReturning(stored), resolverFor(handle, identity))
		must.NoError(t, err)

		user, err := source.User(t.Context(), nil, testScope, handle,
			passkeys.AuthenticatorFlags{BackupEligible: true, BackupState: true})
		must.NoError(t, err)

		credentials := user.WebAuthnCredentials()
		must.SliceLen(t, 1, credentials)
		test.True(t, credentials[0].Flags.BackupEligible)
		test.True(t, credentials[0].Flags.BackupState)
	})

	T.Run("a handle of no bytes is refused before the resolver is called", func(t *testing.T) {
		t.Parallel()

		called := false
		source, err := passkeys.NewUserSource(storeReturning(nil),
			func(context.Context, []byte) (passkeys.UserIdentity, error) {
				called = true

				return identity, nil
			})
		must.NoError(t, err)

		_, err = source.User(t.Context(), nil, testScope, nil, passkeys.AuthenticatorFlags{})
		must.ErrorIs(t, err, passkeys.ErrEmptyHandle)
		test.False(t, called)
	})

	T.Run("the resolver's own refusal comes back unchanged", func(t *testing.T) {
		t.Parallel()

		source, err := passkeys.NewUserSource(storeReturning(nil), resolverFor(handle, identity))
		must.NoError(t, err)

		// This package has no idea which of a consumer's failures "no such user"
		// is, so wrapping it in a sentinel of its own would hide the answer the
		// consumer already gave.
		_, err = source.User(t.Context(), nil, testScope, []byte("somebody else"), passkeys.AuthenticatorFlags{})
		must.ErrorIs(t, err, errNoSuchUser)
	})

	T.Run("a resolver that names nobody is refused", func(t *testing.T) {
		t.Parallel()

		source, err := passkeys.NewUserSource(storeReturning(nil),
			func(context.Context, []byte) (passkeys.UserIdentity, error) {
				return passkeys.UserIdentity{}, nil
			})
		must.NoError(t, err)

		// An empty user id would read as a wildcard at the store, so it is
		// refused here rather than turned into a list of whatever rows a
		// consumer happened to write with one.
		_, err = source.User(t.Context(), nil, testScope, handle, passkeys.AuthenticatorFlags{})
		must.ErrorIs(t, err, passkeys.ErrEmptyUserID)
	})
}

func TestUserSource_DiscoverableUserHandler(T *testing.T) {
	T.Parallel()

	handle := []byte("opaque-handle")
	identity := passkeys.UserIdentity{UserID: "user_1", Name: "grace", DisplayName: "Grace Hopper"}

	T.Run("the handler resolves by the handle, not by the credential", func(t *testing.T) {
		t.Parallel()

		stored := []*passkeys.Credential{
			{BelongsToUser: "user_1", CredentialID: []byte{0x01}, PublicKey: []byte{0x02}},
		}

		source, err := passkeys.NewUserSource(storeReturning(stored), resolverFor(handle, identity))
		must.NoError(t, err)

		handler := source.DiscoverableUserHandler(t.Context(), nil, testScope, passkeys.AuthenticatorFlags{})

		// The raw credential ID is deliberately ignored: the handle is what says
		// who the authenticator is asserting for, and keying on the credential
		// would answer with whoever that credential belongs to instead.
		user, err := handler([]byte("some raw credential id"), handle)
		must.NoError(t, err)
		test.Eq(t, handle, user.WebAuthnID())
	})

	T.Run("a handle the directory does not know", func(t *testing.T) {
		t.Parallel()

		source, err := passkeys.NewUserSource(storeReturning(nil), resolverFor(handle, identity))
		must.NoError(t, err)

		handler := source.DiscoverableUserHandler(t.Context(), nil, testScope, passkeys.AuthenticatorFlags{})

		_, err = handler(nil, []byte("somebody else"))
		must.ErrorIs(t, err, errNoSuchUser)
	})
}
