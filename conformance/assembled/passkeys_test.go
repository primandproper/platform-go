package assembled_test

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/passkeys"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/samber/do/v2"
)

// passkeyRelyingParty is the relying party the WebAuthn block verifies
// against. localhost over plain HTTP is a secure context to a browser, and it
// is the origin the virtual authenticator answers from.
func passkeyRelyingParty() webauthn.Config {
	return webauthn.Config{
		RPID:          "localhost",
		RPDisplayName: "Conformance",
		RPOrigins:     []string{"http://localhost"},
	}
}

// registerPasskeyApplication registers what the Passkeys block resolves from
// the application: who a WebAuthn handle names, who a typed username names,
// and what an enrollment must pass.
//
// A handle is the user's ID, which is passkeys/grpc's default. Both lookups
// read the global directory, which is where the passkey surface's default scope
// resolver places every login and so where the suite enrolls everybody;
// passkeys.UserResolver is handed no scope, so a deployment placing logins
// elsewhere reads its own. The gate admits every live session, which is the
// named choice for a deployment that has decided a signed-in caller may add a
// passkey.
func registerPasskeyApplication(i do.Injector) {
	do.Provide(i, func(i do.Injector) (passkeys.UserResolver, error) {
		db := do.MustInvoke[database.Client](i)
		store := do.MustInvoke[identity.Store](i)

		return func(ctx context.Context, handle []byte) (passkeys.UserIdentity, error) {
			user, err := store.GetUser(ctx, db.Reader(), tenancy.Global(), string(handle))
			if err != nil {
				return passkeys.UserIdentity{}, err
			}

			return passkeys.UserIdentity{UserID: user.ID, Name: user.Username, DisplayName: user.Username}, nil
		}, nil
	})

	do.Provide(i, func(i do.Injector) (passkeys.UsernameResolver, error) {
		db := do.MustInvoke[database.Client](i)
		store := do.MustInvoke[identity.Store](i)

		return func(ctx context.Context, scope tenancy.Scope, username string) ([]byte, error) {
			user, err := store.GetUserByUsername(ctx, db.Reader(), scope, username)
			if platformerrors.Is(err, identity.ErrUserNotFound) {
				return nil, passkeys.ErrUnknownUsername
			}

			if err != nil {
				return nil, err
			}

			return []byte(user.ID), nil
		}, nil
	})

	do.ProvideValue[passkeys.EnrollmentGate](i, passkeys.AdmitEveryEnrollment)
}
