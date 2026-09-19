package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/database"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRegisterWithoutAPasswordThenSignInWithALink is the flow this surface was
// added for, end to end and over the wire: somebody arrives naming no password
// and gets in on one mail.
//
// Before it, a passwordless registration produced an account whose only routes
// were attaching a password or enrolling a passkey — neither of which is "type
// your email, click the link, you are in".
//
// The one step that is not an RPC is reading the mail, exactly as it is for
// verification: the secret goes to the person the account is about and never
// into a response, so the test plays the mail client.
func TestRegisterWithoutAPasswordThenSignInWithALink(T *testing.T) {
	T.Parallel()

	h := newMagicLinkHarness(T, nil)

	registered, err := h.client.Register(asUser(h.rootCtx, h.user.ID), &signinpb.RegisterRequest{
		User: &identitypb.UserRegistrationInput{
			Username:     "ada",
			EmailAddress: "ada@example.com",
		},
		Account:    &identitypb.AccountCreationInput{Name: "Ada's"},
		OwnerRoles: []string{"owner"},
		Credential: &signinpb.RegisterRequest_NoPassword{NoPassword: &signinpb.NoPassword{}},
	})
	must.NoError(T, err)
	must.NotNil(T, registered.GetRegistration())

	// One mail, and the response that asked for it says nothing at all.
	_, err = h.client.RequestMagicLink(h.rootCtx, &signinpb.RequestMagicLinkRequest{
		EmailAddress: "ada@example.com",
	})
	must.NoError(T, err)
	must.EqOp(T, 1, h.mailer.count())

	// The link they were mailed, presented by a client with nobody on it: they
	// hold no credential, so the RPC that signs them in cannot require one.
	signedIn, err := h.client.RedeemMagicLink(h.rootCtx, &signinpb.RedeemMagicLinkRequest{
		Token: h.mailer.secret(T),
	})
	must.NoError(T, err)

	test.NotEqOp(T, "", signedIn.GetToken().GetToken())

	// And the click promoted them, which is the half that makes one mail enough:
	// they arrived unverified, and nothing else moved that standing.
	after, err := h.store.GetUserByUsername(T.Context(), h.db.Reader(), testScope, "ada")
	must.NoError(T, err)

	test.EqOp(T, identity.StatusGood, after.AccountStatus)
	test.NotNil(T, after.EmailAddressVerifiedAt)
}

// TestRequestMagicLinkSaysNothingAboutAnAddress is the enumeration defense at
// the transport, which is where a consumer is likeliest to undo it.
//
// An address nobody holds and one somebody does produce the same empty response
// and the same OK status. A handler that answered one with NotFound would put
// the oracle back one layer up, where neither the service's padding nor its
// silence reaches.
func TestRequestMagicLinkSaysNothingAboutAnAddress(T *testing.T) {
	T.Parallel()

	h := newMagicLinkHarness(T, nil)

	stranger, err := h.client.RequestMagicLink(h.rootCtx, &signinpb.RequestMagicLinkRequest{
		EmailAddress: "nobody@example.com",
	})
	must.NoError(T, err)
	must.NotNil(T, stranger)
	test.EqOp(T, 0, h.mailer.count())

	known, err := h.client.RequestMagicLink(h.rootCtx, &signinpb.RequestMagicLinkRequest{
		EmailAddress: h.user.EmailAddress,
	})
	must.NoError(T, err)
	must.NotNil(T, known)
	test.EqOp(T, 1, h.mailer.count())
}

// TestMagicLinkRPCsAreAnonymous is the declaration both halves rest on: the
// person asking cannot sign in yet, so requiring a principal would require a
// sign-in from somebody who has no way to make one.
func TestMagicLinkRPCsAreAnonymous(T *testing.T) {
	T.Parallel()

	h := newMagicLinkHarness(T, nil)

	// No caller on either context, and neither is refused for that reason.
	_, err := h.client.RequestMagicLink(h.rootCtx, &signinpb.RequestMagicLinkRequest{
		EmailAddress: h.user.EmailAddress,
	})
	must.NoError(T, err)

	_, err = h.client.RedeemMagicLink(h.rootCtx, &signinpb.RedeemMagicLinkRequest{
		Token: h.mailer.secret(T),
	})
	test.NoError(T, err)
}

// TestRedeemMagicLinkCollapsesItsRefusals pins what a client is told, which is
// one answer for four facts and the same answer a wrong password gets.
func TestRedeemMagicLinkCollapsesItsRefusals(T *testing.T) {
	T.Parallel()

	h := newMagicLinkHarness(T, nil)

	_, err := h.client.RedeemMagicLink(h.rootCtx, &signinpb.RedeemMagicLinkRequest{
		Token: "nothing-was-ever-minted-for-this",
	})

	test.ErrorIs(T, err, signin.ErrInvalidMagicLink)
	test.ErrorIs(T, err, signin.ErrInvalidCredentials)
	test.EqOp(T, codes.Unauthenticated, status.Code(err))

	// A spent link lands in the same place, which is the property that keeps the
	// four from being told apart by whoever is guessing.
	_, err = h.client.RequestMagicLink(h.rootCtx, &signinpb.RequestMagicLinkRequest{
		EmailAddress: h.user.EmailAddress,
	})
	must.NoError(T, err)

	token := h.mailer.secret(T)

	_, err = h.client.RedeemMagicLink(h.rootCtx, &signinpb.RedeemMagicLinkRequest{Token: token})
	must.NoError(T, err)

	_, err = h.client.RedeemMagicLink(h.rootCtx, &signinpb.RedeemMagicLinkRequest{Token: token})

	test.ErrorIs(T, err, signin.ErrInvalidMagicLink)
	test.EqOp(T, codes.Unauthenticated, status.Code(err))
}

// TestRedeemMagicLinkRefusesABannedUser pins that the status refusals reach the
// wire unchanged, and that an email does not overturn an operator's decision.
func TestRedeemMagicLinkRefusesABannedUser(T *testing.T) {
	T.Parallel()

	h := newMagicLinkHarness(T, nil)

	_, err := h.client.RequestMagicLink(h.rootCtx, &signinpb.RequestMagicLinkRequest{
		EmailAddress: h.user.EmailAddress,
	})
	must.NoError(T, err)

	token := h.mailer.secret(T)

	must.NoError(T, h.db.WithTransaction(T.Context(), func(tx database.Tx) error {
		return h.store.UpdateUserAccountStatus(T.Context(), tx, testScope, h.user.ID,
			identity.StatusBanned, "for cause")
	}))

	_, err = h.client.RedeemMagicLink(h.rootCtx, &signinpb.RedeemMagicLinkRequest{Token: token})

	test.ErrorIs(T, err, signin.ErrUserBanned)
	test.EqOp(T, codes.PermissionDenied, status.Code(err))
}
