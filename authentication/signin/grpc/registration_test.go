package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// registrationInput is the wire half of a registration, as a client assembles
// it.
func registrationInput(username string) *signinpb.RegisterRequest {
	return &signinpb.RegisterRequest{
		User: &identitypb.UserRegistrationInput{
			Username:     username,
			EmailAddress: username + "@example.com",
			FirstName:    "New",
		},
		Account:    &identitypb.AccountCreationInput{Name: username + "'s"},
		OwnerRoles: []string{"owner"},
	}
}

// TestRegisterThenVerifyThenSignIn is the property this surface exists for, and
// the one that could not be had before it: somebody registered over gRPC can
// sign in over gRPC.
//
// The one step that is not an RPC is reading the mail. A verification link goes
// to the person the account is about, and whoever clicks it is not the client
// that called Register — so the secret is never in a response, and this test
// plays the mail client by naming the generator the service mints it with.
func TestRegisterThenVerifyThenSignIn(T *testing.T) {
	T.Parallel()

	secrets := &mailedSecrets{secret: "the-token-in-their-inbox"}
	h := newHarness(T, []signin.ServiceOption{signin.WithSecretGenerator(secrets)})

	registered, err := h.client.Register(asUser(h.rootCtx, h.user.ID), &signinpb.RegisterRequest{
		User: &identitypb.UserRegistrationInput{
			Username:     "ada",
			EmailAddress: "ada@example.com",
		},
		Account:    &identitypb.AccountCreationInput{Name: "Ada's"},
		OwnerRoles: []string{"owner"},
		Credential: &signinpb.RegisterRequest_Password{Password: "hunter2 hunter2"},
	})
	must.NoError(T, err)
	must.NotNil(T, registered.GetRegistration())
	test.EqOp(T, "ada", registered.GetRegistration().GetUser().GetUsername())
	must.NotNil(T, registered.GetRegistration().GetAccount())
	must.NotNil(T, registered.GetRegistration().GetMembership())

	credentials := &signinpb.Credentials{Username: "ada", Password: "hunter2 hunter2"}

	// Unverified, so the door refuses them although the password is right.
	_, err = h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{Credentials: credentials})
	test.ErrorIs(T, err, signin.ErrUserUnverified)
	test.EqOp(T, codes.FailedPrecondition, status.Code(err))

	// The link they were mailed, presented by a client with nobody on it: they
	// have no credential yet, so the RPC that finishes their registration cannot
	// require one.
	_, err = h.client.VerifyEmailAddress(h.rootCtx, &signinpb.VerifyEmailAddressRequest{
		Token: secrets.secret,
	})
	must.NoError(T, err)

	signedIn, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{Credentials: credentials})
	must.NoError(T, err)
	test.NotEqOp(T, "", signedIn.GetToken().GetToken())
}

// TestRegisterWithoutAPasswordThenAttachOne is the other arrival: somebody who
// named no password, claiming their account from the same mail.
func TestRegisterWithoutAPasswordThenAttachOne(T *testing.T) {
	T.Parallel()

	secrets := &mailedSecrets{secret: "the-token-in-their-inbox"}
	h := newHarness(T, []signin.ServiceOption{signin.WithSecretGenerator(secrets)})

	request := registrationInput("ada")
	request.Credential = &signinpb.RegisterRequest_NoPassword{NoPassword: &signinpb.NoPassword{}}

	_, err := h.client.Register(asUser(h.rootCtx, h.user.ID), request)
	must.NoError(T, err)

	// Attaching does not spend the link, so the same click goes on to verify.
	_, err = h.client.AttachPassword(h.rootCtx, &signinpb.AttachPasswordRequest{
		Token:       secrets.secret,
		NewPassword: "hunter2 hunter2",
	})
	must.NoError(T, err)

	_, err = h.client.VerifyEmailAddress(h.rootCtx, &signinpb.VerifyEmailAddressRequest{Token: secrets.secret})
	must.NoError(T, err)

	signedIn, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
		Credentials: &signinpb.Credentials{Username: "ada", Password: "hunter2 hunter2"},
	})
	must.NoError(T, err)
	test.NotEqOp(T, "", signedIn.GetToken().GetToken())
}

// TestRegisterNamingNoCredential is the refusal the oneof exists for. A client
// that populated neither arm is told to correct its request rather than having
// one chosen for it.
func TestRegisterNamingNoCredential(T *testing.T) {
	T.Parallel()

	h := newHarness(T, nil)

	_, err := h.client.Register(asUser(h.rootCtx, h.user.ID), registrationInput("ada"))
	test.ErrorIs(T, err, signin.ErrNoCredentialNamed)
	test.EqOp(T, codes.InvalidArgument, status.Code(err))

	// And no user landed, which is what makes this a refusal rather than a
	// half-finished registration.
	_, err = h.store.GetUserByUsername(h.rootCtx, h.db.Reader(), testScope, "ada")
	test.ErrorIs(T, err, identity.ErrUserNotFound)
}

// TestRegisterRequiresACaller is the one authenticated RPC here whose subject is
// not the caller. It is the registrar's own principal, for the reason identity's
// namesake requires one: an open sign-up is a flow with policy in it, and this
// service holds none of that.
func TestRegisterRequiresACaller(T *testing.T) {
	T.Parallel()

	h := newHarness(T, nil)

	request := registrationInput("ada")
	request.Credential = &signinpb.RegisterRequest_Password{Password: "hunter2 hunter2"}

	_, err := h.client.Register(h.rootCtx, request)
	test.EqOp(T, codes.Unauthenticated, status.Code(err))
}

// TestAttachPasswordAndVerifyAreAnonymous is the other half of that decision,
// and the reason it could not be otherwise: the person these two RPCs are about
// cannot be signed in, so requiring a principal would close the only door they
// have.
func TestAttachPasswordAndVerifyAreAnonymous(T *testing.T) {
	T.Parallel()

	secrets := &mailedSecrets{secret: "the-token-in-their-inbox"}
	h := newHarness(T, []signin.ServiceOption{signin.WithSecretGenerator(secrets)})

	request := registrationInput("ada")
	request.Credential = &signinpb.RegisterRequest_NoPassword{NoPassword: &signinpb.NoPassword{}}

	_, err := h.client.Register(asUser(h.rootCtx, h.user.ID), request)
	must.NoError(T, err)

	// No caller on either request, and neither is refused for it.
	_, err = h.client.AttachPassword(h.rootCtx, &signinpb.AttachPasswordRequest{
		Token:       secrets.secret,
		NewPassword: "hunter2 hunter2",
	})
	must.NoError(T, err)

	_, err = h.client.VerifyEmailAddress(h.rootCtx, &signinpb.VerifyEmailAddressRequest{Token: secrets.secret})
	must.NoError(T, err)
}

// TestAttachPasswordRefusesAnAccountThatHasOne pins the refusal that keeps a
// verification link from being a password reset, over the transport that would
// otherwise expose it to anybody holding a link.
func TestAttachPasswordRefusesAnAccountThatHasOne(T *testing.T) {
	T.Parallel()

	secrets := &mailedSecrets{secret: "the-token-in-their-inbox"}
	h := newHarness(T, []signin.ServiceOption{signin.WithSecretGenerator(secrets)})

	request := registrationInput("ada")
	request.Credential = &signinpb.RegisterRequest_Password{Password: "hunter2 hunter2"}

	_, err := h.client.Register(asUser(h.rootCtx, h.user.ID), request)
	must.NoError(T, err)

	_, err = h.client.AttachPassword(h.rootCtx, &signinpb.AttachPasswordRequest{
		Token:       secrets.secret,
		NewPassword: "a password somebody else chose",
	})
	test.ErrorIs(T, err, signin.ErrPasswordAlreadySet)
	test.EqOp(T, codes.FailedPrecondition, status.Code(err))
}

// TestVerifyEmailAddressWithABadToken pins that the four ways a link fails are
// one answer on the wire, and that it is the answer a wrong password gets.
func TestVerifyEmailAddressWithABadToken(T *testing.T) {
	T.Parallel()

	h := newHarness(T, nil)

	_, err := h.client.VerifyEmailAddress(h.rootCtx, &signinpb.VerifyEmailAddressRequest{Token: "never-issued"})
	test.ErrorIs(T, err, signin.ErrInvalidVerificationToken)
	test.ErrorIs(T, err, signin.ErrInvalidCredentials)
	test.EqOp(T, codes.Unauthenticated, status.Code(err))

	// The words are ErrInvalidCredentials's own, which is what keeps a link that
	// expired from being told apart from one that was never issued.
	test.EqOp(T, signin.ErrInvalidCredentials.Error(), status.Convert(err).Message())
}

// TestRegisteredCarriesNoVerificationToken is the schema promise, asserted
// against the message rather than against the documentation: the secret that
// claims an account is never handed to whoever called Register.
func TestRegisteredCarriesNoVerificationToken(T *testing.T) {
	T.Parallel()

	secrets := &mailedSecrets{secret: "the-token-in-their-inbox"}
	h := newHarness(T, []signin.ServiceOption{signin.WithSecretGenerator(secrets)})

	request := registrationInput("ada")
	request.Credential = &signinpb.RegisterRequest_Password{Password: "hunter2 hunter2"}

	registered, err := h.client.Register(asUser(h.rootCtx, h.user.ID), request)
	must.NoError(T, err)

	// Nothing on the response says it, and there is no field that could: the
	// whole rendered message is checked rather than the fields somebody thought
	// to name.
	test.StrNotContains(T, registered.String(), secrets.secret)
}
