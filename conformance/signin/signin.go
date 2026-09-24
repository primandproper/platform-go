package signin

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	domain "github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	pquernatotp "github.com/pquerna/otp/totp"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Suite is the sign-in surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    "signin",
		Mounted: func(s conformance.Surfaces) bool { return s.SignIn != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("doors", func(t *testing.T) {
		t.Parallel()
		doors(t, s)
	})
	t.Run("registration", func(t *testing.T) {
		t.Parallel()
		registration(t, s)
	})
	t.Run("refresh", func(t *testing.T) {
		t.Parallel()
		refresh(t, s)
	})
	t.Run("self", func(t *testing.T) {
		t.Parallel()
		self(t, s)
	})
	t.Run("magic links", func(t *testing.T) {
		t.Parallel()
		magicLinks(t, s)
	})
}

// The reasons docs/client-contract.md lists for the refusals asserted here,
// spelled once so that a typo in one assertion cannot pass by comparing
// against a typo in another.
const (
	reasonInvalidCredentials      = "INVALID_CREDENTIALS" //nolint:gosec // G101: a refusal's name, not a credential.
	reasonSecondFactorRequired    = "SECOND_FACTOR_REQUIRED"
	reasonSecondFactorNotEnrolled = "SECOND_FACTOR_NOT_ENROLLED"
	reasonUserUnverified          = "USER_UNVERIFIED"
	reasonNotAnAdministrator      = "NOT_AN_ADMINISTRATOR"
	reasonAdminSignInUnavailable  = "ADMIN_SIGNIN_UNAVAILABLE"
	reasonPasswordAlreadySet      = "PASSWORD_ALREADY_SET"
	reasonNoCredentialNamed       = "NO_CREDENTIAL_NAMED" //nolint:gosec // G101: a refusal's name, not a credential.
)

// password is what the registrations here choose, and newPassword is what a
// change or a reset sets. Long enough that no deployment's strength rule refuses
// either, since the assertions are about the doors rather than the rule — and
// wrongPassword is the guess, which is nobody's.
const (
	password      = "a password long enough for anybody's rule"
	newPassword   = "a whole new password, long enough for anybody"
	wrongPassword = "not the password"
)

// reason is the client-safe reason a refusal carried in signin's domain, or
// empty where it carried none.
//
// The domain is checked as well as the type, because the contract names both:
// a reason from somebody else's domain is not one a sign-in client is told to
// branch on.
func reason(err error) string {
	info, ok := grpcerrors.ClientReasonFromStatus(err)
	if !ok || info.GetDomain() != domain.ClientReasonDomain {
		return ""
	}

	return info.GetReason()
}

// refused asserts err is a refusal with the code and reason the contract lists
// for it.
func refused(t *testing.T, err error, code codes.Code, want string) {
	t.Helper()

	must.Error(t, err, must.Sprintf("expected a refusal answering %s", want))
	test.EqOp(t, code, status.Code(err))
	test.EqOp(t, want, reason(err), test.Sprintf("the refusal carried reason %q (%v)", reason(err), err))
}

// indistinguishable asserts two refusals are the same answer on every channel a
// client reads: the code, the message and the reason.
func indistinguishable(t *testing.T, want, got error, what string) {
	t.Helper()

	must.Error(t, want)
	must.Error(t, got)

	test.EqOp(t, status.Code(want), status.Code(got), test.Sprintf("%s: the codes differ", what))
	test.EqOp(t, status.Convert(want).Message(), status.Convert(got).Message(),
		test.Sprintf("%s: the messages differ", what))
	test.EqOp(t, reason(want), reason(got), test.Sprintf("%s: the reasons differ", what))
}

// anonymous is the sign-in surface as a client with nobody on it reaches it —
// which is how every door here is reached, since the person on the other end
// has not signed in yet.
func anonymous(t *testing.T, s *conformance.Session) signinpb.SignInServiceClient {
	t.Helper()

	open := s.Seams().Anonymous
	if open == nil {
		t.Skip("conformance: this subject supplies no anonymous connection, so the doors a person signs in through cannot be reached as that person")
	}

	conn, err := open(t.Context())
	must.NoError(t, err, must.Sprint("opening a connection with nobody on it"))

	return signinpb.NewSignInServiceClient(conn)
}

// registrar is a caller in the directory the anonymous doors place a request
// in, which is who registers somebody for them to sign in. See the package
// documentation for why that directory is the global one.
func registrar(t *testing.T, s *conformance.Session) *conformance.Subject {
	t.Helper()

	return s.Subject(t, conformance.InTenant(tenancy.Global()))
}

// registrant is somebody registered over the wire: what they would type to sign
// in, and what the registration answered.
type registrant struct {
	username  string
	email     string
	accountID string
}

// freshEmail is an address nobody has used. Lowercase, so that the address a
// mail was sent to is the address it was registered with, whatever the
// deployment folds.
func freshEmail() string { return identifiers.New() + "@conformance.invalid" }

// registrationRequest is a registration for somebody nobody has registered,
// naming no credential; each caller names the one it is about.
func registrationRequest() *signinpb.RegisterRequest {
	username := "conf_" + identifiers.New()

	return &signinpb.RegisterRequest{
		User: &identitypb.UserRegistrationInput{
			Username:     username,
			EmailAddress: freshEmail(),
			FirstName:    "Some",
		},
		Account:    &identitypb.AccountCreationInput{Name: username + "'s"},
		OwnerRoles: []string{"owner"},
	}
}

// withPassword names password as a registration's credential.
func withPassword(request *signinpb.RegisterRequest) *signinpb.RegisterRequest {
	request.Credential = &signinpb.RegisterRequest_Password{Password: password}

	return request
}

// withNoPassword names the deliberate absence of one.
func withNoPassword(request *signinpb.RegisterRequest) *signinpb.RegisterRequest {
	request.Credential = &signinpb.RegisterRequest_NoPassword{NoPassword: &signinpb.NoPassword{}}

	return request
}

// register registers somebody through sign-in's own door, as a registrar in the
// global directory, and fails the test if the registration is refused.
func register(t *testing.T, s *conformance.Session, request *signinpb.RegisterRequest) (*registrant, *signinpb.Registered) {
	t.Helper()

	by := registrar(t, s)

	response, err := by.Surfaces.SignIn.Register(by.Context(t.Context()), request)
	must.NoError(t, err, must.Sprint("registering somebody through sign-in"))

	registered := response.GetRegistration()
	must.NotNil(t, registered, must.Sprint("a registration answered with no registration"))
	must.NotEqOp(t, "", registered.GetUser().GetId(), must.Sprint("a registration answered with no user"))

	return &registrant{
		username:  request.GetUser().GetUsername(),
		email:     request.GetUser().GetEmailAddress(),
		accountID: registered.GetAccount().GetId(),
	}, registered
}

// mailedVerification is the verification link the deployment mailed to an
// address, or a skip where the subject cannot say.
func mailedVerification(t *testing.T, s *conformance.Session, emailAddress string) string {
	t.Helper()

	read := s.Seams().Actions.VerificationToken
	s.NeedsAction(t, read != nil, "verification token")

	token, err := read(t.Context(), tenancy.Global(), emailAddress)
	must.NoError(t, err, must.Sprint("reading the verification link the deployment mailed"))
	must.NotEqOp(t, "", token, must.Sprint("the deployment mailed an empty verification link"))

	return token
}

// verify answers the verification link a registrant was mailed, as they do: from
// a client with nobody on it.
func verify(t *testing.T, s *conformance.Session, anon signinpb.SignInServiceClient, who *registrant) {
	t.Helper()

	_, err := anon.VerifyEmailAddress(t.Context(), &signinpb.VerifyEmailAddressRequest{
		Token: mailedVerification(t, s, who.email),
	})
	must.NoError(t, err, must.Sprint("the verification link the deployment mailed did not verify"))
}

// signInAs is somebody registered with a password and verified, ready to sign in.
func signInAs(t *testing.T, s *conformance.Session, anon signinpb.SignInServiceClient) *registrant {
	t.Helper()

	who, _ := register(t, s, withPassword(registrationRequest()))
	verify(t, s, anon, who)

	return who
}

// login signs in through the password door.
func login(
	ctx context.Context,
	client signinpb.SignInServiceClient,
	username, secret, code string,
) (*signinpb.IssuedToken, error) {
	response, err := client.LoginForToken(ctx, &signinpb.LoginForTokenRequest{
		Credentials: &signinpb.Credentials{Username: username, Password: secret, TotpCode: code},
	})
	if err != nil {
		return nil, err
	}

	return response.GetToken(), nil
}

// loggedIn signs in and fails the test if the door refuses.
func loggedIn(t *testing.T, client signinpb.SignInServiceClient, username, secret string) *signinpb.IssuedToken {
	t.Helper()

	issued, err := login(t.Context(), client, username, secret, "")
	must.NoError(t, err, must.Sprint("signing in with the right password"))
	must.NotNil(t, issued, must.Sprint("a sign-in answered with no token"))

	return issued
}

// passworded is a signed-in caller in the global directory whose password the
// suite knows, and that password.
//
// The password is set through the reset flow rather than written, because a
// seam is an action rather than a row: the caller forgot a password they never
// had, was mailed a link, and chose one. It therefore needs the password reset
// surface and its action, and skips without either.
func passworded(t *testing.T, s *conformance.Session) (*conformance.Subject, *identitypb.User) {
	t.Helper()

	sub := registrar(t, s)

	if sub.Surfaces.Identity == nil || sub.Surfaces.PasswordReset == nil {
		t.Skip("conformance: this subject mounts no identity or password reset surface, so a caller cannot be given a password the suite knows")
	}

	found, err := sub.Surfaces.Identity.GetUser(sub.Context(t.Context()),
		&identitypb.GetUserRequest{UserId: sub.UserID})
	must.NoError(t, err, must.Sprint("a caller could not read its own user"))

	user := found.GetUser()
	must.NotEqOp(t, "", user.GetEmailAddress(), must.Sprint("the caller has no address to reset through"))

	read := s.Seams().Actions.PasswordResetToken
	s.NeedsAction(t, read != nil, "password reset token")

	_, err = sub.Surfaces.PasswordReset.RequestPasswordReset(t.Context(),
		&passwordresetpb.RequestPasswordResetRequest{EmailAddress: user.GetEmailAddress()})
	must.NoError(t, err, must.Sprint("requesting a reset link"))

	secret, err := read(t.Context(), sub.Scope, user.GetEmailAddress())
	must.NoError(t, err, must.Sprint("reading the reset link the deployment mailed"))

	_, err = sub.Surfaces.PasswordReset.CompletePasswordReset(t.Context(),
		&passwordresetpb.CompletePasswordResetRequest{Token: secret, NewPassword: password})
	must.NoError(t, err, must.Sprint("setting the caller's password through the link"))

	return sub, user
}

// code is the second-factor code an authenticator app shows for secret now.
func code(t *testing.T, secret string) string {
	t.Helper()

	value, err := pquernatotp.GenerateCode(secret, time.Now().UTC())
	must.NoError(t, err, must.Sprint("computing a second-factor code"))

	return value
}

// wrongCode is a well-formed code that is not secret's current one. Derived
// from the right code rather than chosen, so it cannot be right by chance.
func wrongCode(t *testing.T, secret string) string {
	t.Helper()

	right := []byte(code(t, secret))
	right[0] = '0' + (right[0]-'0'+1)%10

	return string(right)
}

// enroll gives a signed-in caller a proven second factor over the wire, and
// returns its secret.
func enroll(t *testing.T, sub *conformance.Subject) string {
	t.Helper()

	refreshed, err := sub.Surfaces.SignIn.RefreshTOTPSecret(sub.Context(t.Context()),
		&signinpb.RefreshTOTPSecretRequest{CurrentPassword: password})
	must.NoError(t, err, must.Sprint("issuing a second-factor secret"))
	must.NotEqOp(t, "", refreshed.GetSecret(), must.Sprint("an issued second factor had no secret"))

	_, err = sub.Surfaces.SignIn.VerifyTOTPSecret(sub.Context(t.Context()),
		&signinpb.VerifyTOTPSecretRequest{TotpCode: code(t, refreshed.GetSecret())})
	must.NoError(t, err, must.Sprint("proving the issued second factor"))

	return refreshed.GetSecret()
}
