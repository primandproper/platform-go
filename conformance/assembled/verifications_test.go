package assembled_test

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// AfterRegister is where a consumer queues the verification mail: the
// registration's own transaction, which is the one place the link's secret is
// readable, since the column holds only a digest. This remembers it by address
// instead of mailing it.
//
// A registration that minted no link — every caller the subject factory mints
// is registered in good standing, with none — has nothing to mail, and nothing
// is remembered for it.
func (r *invitationTokens) AfterRegister(_ context.Context, _ database.Tx, _ tenancy.Scope, registration *identity.Registration) error {
	r.rememberVerification(registration.User, registration.EmailAddressVerificationToken)

	return nil
}

// AfterRegisterWithInvitation is AfterRegister for a registrant who answered an
// invitation, which mails the same link.
func (r *invitationTokens) AfterRegisterWithInvitation(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	registration *identity.InvitedRegistration,
) error {
	r.rememberVerification(registration.User, registration.EmailAddressVerificationToken)

	return nil
}

func (r *invitationTokens) rememberVerification(user *identity.User, token string) {
	if user == nil || token == "" {
		return
	}

	r.verifications.Store(user.EmailAddress, token)
}

var errNoVerificationMailed = platformerrors.New("conformance harness: no verification link reached that address")

// verificationToken is the VerificationToken action.
func (r *invitationTokens) verificationToken(_ context.Context, _ tenancy.Scope, emailAddress string) (string, error) {
	value, ok := r.verifications.Load(emailAddress)
	if !ok {
		return "", errNoVerificationMailed
	}

	token, ok := value.(string)
	if !ok {
		return "", errNoVerificationMailed
	}

	return token, nil
}
