package assembled_test

import (
	"context"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// invitationTokens is this harness's deployment-side half of an invitation: the
// AfterInvite hook a consumer would use to mail the link, remembering the token
// instead. It is registered as the service's identity.Hooks, so what it sees is
// exactly what a consumer's hook would be handed.
//
// It is also where a registrant's verification link goes to be mailed, since a
// deployment has one identity.Hooks and the two registration hooks are the only
// places that secret is readable — see verifications_test.go.
type invitationTokens struct {
	identity.NoopHooks
	tokens        sync.Map
	verifications sync.Map
}

var _ identity.Hooks = (*invitationTokens)(nil)

func (r *invitationTokens) AfterInvite(_ context.Context, _ database.Tx, _ tenancy.Scope, invitation *identity.Invitation) error {
	r.tokens.Store(invitation.ID, invitation.Token)

	return nil
}

var errNoInvitationSeen = platformerrors.New("conformance harness: no invitation by that id reached the hook")

// token is the InvitationToken action.
func (r *invitationTokens) token(_ context.Context, _ tenancy.Scope, invitationID string) (string, error) {
	value, ok := r.tokens.Load(invitationID)
	if !ok {
		return "", errNoInvitationSeen
	}

	token, ok := value.(string)
	if !ok {
		return "", errNoInvitationSeen
	}

	return token, nil
}

// verifyEmail is the EmailVerified action: the two steps a verification link
// takes, a token set and then the same token presented back, in one
// transaction on the deployment's own client.
func verifyEmail(db database.Client, store identity.Store) func(context.Context, tenancy.Scope, string) error {
	return func(ctx context.Context, scope tenancy.Scope, userID string) error {
		const token = "conformance-verification"

		return db.WithTransaction(context.WithoutCancel(ctx), func(tx database.Tx) error {
			if err := store.SetUserEmailAddressVerificationToken(ctx, tx, scope, userID, token,
				time.Now().UTC().Add(time.Hour)); err != nil {
				return err
			}

			return store.MarkUserEmailAddressVerified(ctx, tx, scope, userID, token)
		})
	}
}
