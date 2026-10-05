package signin

import (
	"context"
	"slices"
	"testing"
	"time"

	domain "github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/identity/identitypb"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	pquernatotp "github.com/pquerna/otp/totp"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "signin"

// The calls this suite makes, as the names a caller is minted to make them by,
// or that a door is reached by with nobody on it.
const (
	adminLoginForToken                = signinpb.SignInService_AdminLoginForToken_FullMethodName
	attachPassword                    = signinpb.SignInService_AttachPassword_FullMethodName
	endOtherSignIns                   = signinpb.SignInService_EndOtherSignIns_FullMethodName
	endSignIn                         = signinpb.SignInService_EndSignIn_FullMethodName
	exchangeRefreshToken              = signinpb.SignInService_ExchangeRefreshToken_FullMethodName
	getAuthStatus                     = signinpb.SignInService_GetAuthStatus_FullMethodName
	getSelf                           = signinpb.SignInService_GetSelf_FullMethodName
	listSignIns                       = signinpb.SignInService_ListSignIns_FullMethodName
	loginForToken                     = signinpb.SignInService_LoginForToken_FullMethodName
	redeemMagicLink                   = signinpb.SignInService_RedeemMagicLink_FullMethodName
	refreshTOTPSecret                 = signinpb.SignInService_RefreshTOTPSecret_FullMethodName
	registerUser                      = signinpb.SignInService_Register_FullMethodName
	requestHandleReminder             = signinpb.SignInService_RequestHandleReminder_FullMethodName
	requestMagicLink                  = signinpb.SignInService_RequestMagicLink_FullMethodName
	requestVerificationEmail          = signinpb.SignInService_RequestVerificationEmail_FullMethodName
	requestVerificationEmailByAddress = signinpb.SignInService_RequestVerificationEmailByAddress_FullMethodName
	signOut                           = signinpb.SignInService_SignOut_FullMethodName
	signOutEverywhere                 = signinpb.SignInService_SignOutEverywhere_FullMethodName
	switchAccount                     = signinpb.SignInService_SwitchAccount_FullMethodName
	updateEmailAddress                = signinpb.SignInService_UpdateEmailAddress_FullMethodName
	updatePassword                    = signinpb.SignInService_UpdatePassword_FullMethodName
	updateUsername                    = signinpb.SignInService_UpdateUsername_FullMethodName
	verifyEmailAddress                = signinpb.SignInService_VerifyEmailAddress_FullMethodName
	verifyTOTPSecret                  = signinpb.SignInService_VerifyTOTPSecret_FullMethodName
	acceptInvitation                  = identitypb.IdentityService_AcceptInvitation_FullMethodName
	invite                            = identitypb.IdentityService_Invite_FullMethodName
	setUserRequiresPasswordChange     = identitypb.IdentityService_SetUserRequiresPasswordChange_FullMethodName
	setUserServiceRoles               = identitypb.IdentityService_SetUserServiceRoles_FullMethodName
	updateUserAccountStatus           = identitypb.IdentityService_UpdateUserAccountStatus_FullMethodName
)

// identitySurface is the identity suite's name, for the operators here whose
// calls are identity's: the tenant they are asked into is read on that surface.
const identitySurface = "identity"

// Suite is the sign-in surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
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

	t.Run("handle reminders", func(t *testing.T) {
		t.Parallel()
		handleReminders(t, s)
	})

	t.Run("administration", func(t *testing.T) {
		t.Parallel()
		administration(t, s)
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
	reasonEmailAlreadyVerified    = "EMAIL_ADDRESS_ALREADY_VERIFIED"
	reasonNoCredentialNamed       = "NO_CREDENTIAL_NAMED" //nolint:gosec // G101: a refusal's name, not a credential.
	reasonPasswordChangeRequired  = "PASSWORD_CHANGE_REQUIRED"
	reasonSignInNotIdentified     = "SIGN_IN_NOT_IDENTIFIED"
	reasonRegistrationClosed      = "REGISTRATION_CLOSED"
	reasonRegistrationRefused     = "REGISTRATION_REFUSED"
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

// inviteeName is who the invitations here are addressed to. Nothing reads it
// back; an invitation just has to be addressed to somebody.
const inviteeName = "Some Body"

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

// reasons reports whether s's subject carries reasons to its clients, printing
// what goes unasserted where it does not. Every reason comparison here goes
// through it, so a subject that strips them still has every code asserted.
func reasons(t *testing.T, s *conformance.Session) bool {
	t.Helper()

	if !s.Seams().ErrorReasonsStripped {
		return true
	}

	t.Log("conformance: this subject says its edge strips client-safe reasons (Seams.ErrorReasonsStripped), so the reason half of this refusal is not asserted")

	return false
}

// refused asserts err is a refusal with the code and reason the contract lists
// for it. The reason is asserted only where the subject carries one.
func refused(t *testing.T, s *conformance.Session, err error, code codes.Code, want string) {
	t.Helper()

	must.Error(t, err, must.Sprintf("expected a refusal answering %s", want))
	test.EqOp(t, code, status.Code(err))

	if reasons(t, s) {
		test.EqOp(t, want, reason(err), test.Sprintf("the refusal carried reason %q (%v)", reason(err), err))
	}
}

// indistinguishable asserts two refusals are the same answer on every channel a
// client reads: the code, the message and, where the subject carries one, the
// reason.
func indistinguishable(t *testing.T, s *conformance.Session, want, got error, what string) {
	t.Helper()

	must.Error(t, want)
	must.Error(t, got)

	test.EqOp(t, status.Code(want), status.Code(got), test.Sprintf("%s: the codes differ", what))
	test.EqOp(t, status.Convert(want).Message(), status.Convert(got).Message(),
		test.Sprintf("%s: the messages differ", what))

	if reasons(t, s) {
		test.EqOp(t, reason(want), reason(got), test.Sprintf("%s: the reasons differ", what))
	}
}

// anonymous is the sign-in surface as a client with nobody on it reaches it —
// which is how every door here is reached, since the person on the other end
// has not signed in yet.
//
// calls are the doors the test goes on to knock on, and it skips where the
// subject reserves any of them to an operator: a door a deployment keeps to its
// staff is one nobody reaches without signing in.
func anonymous(t *testing.T, s *conformance.Session, calls ...string) signinpb.SignInServiceClient {
	t.Helper()

	s.NeedsPublic(t, calls...)

	open := s.Seams().Anonymous
	if open == nil {
		conformance.Skip(t, "conformance: this subject supplies no anonymous connection, so the doors a person signs in through cannot be reached as that person")
	}

	conn, err := open(t.Context())
	must.NoError(t, err, must.Sprint("opening a connection with nobody on it"))

	return signinpb.NewSignInServiceClient(conn)
}

// member is a caller in the directory the anonymous doors place a request in,
// making methods — an ordinary one unless the subject reserves any of them. See
// the package documentation for why that directory is the global one.
func member(t *testing.T, s *conformance.Session, methods ...string) *conformance.Subject {
	t.Helper()

	return s.Subject(t, conformance.Making(methods...), conformance.InTenant(surface, tenancy.Global()))
}

// registrationDoor is how somebody is registered for the anonymous doors to
// sign in: the sign-up door with nobody on it, which places them in the
// directory those doors are against. Where the subject reserves Register to an
// operator it is an operator in that same directory instead, and where the
// subject closed the door there is no way to register anybody over the wire,
// so the assertion skips.
func registrationDoor(
	t *testing.T,
	s *conformance.Session,
) func(context.Context, *signinpb.RegisterRequest) (*signinpb.RegisterResponse, error) {
	t.Helper()

	if s.Seams().RegistrationClosed {
		conformance.Skip(t, "conformance: this subject closes its sign-up door (Seams.RegistrationClosed), so nobody is registered over the wire; skipping")
	}

	if s.Reserves(registerUser) {
		by := s.Subject(t, conformance.Making(registerUser), conformance.InTenant(surface, tenancy.Global()))

		return func(ctx context.Context, request *signinpb.RegisterRequest) (*signinpb.RegisterResponse, error) {
			return by.Surfaces.SignIn.Register(by.Context(ctx), request)
		}
	}

	anon := anonymous(t, s, registerUser)

	return func(ctx context.Context, request *signinpb.RegisterRequest) (*signinpb.RegisterResponse, error) {
		return anon.Register(ctx, request)
	}
}

// registrant is somebody registered over the wire: what they would type to sign
// in, and what the registration answered.
type registrant struct {
	userID    string
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
//
// It accepts every agreement. Whether any are required is the deployment's,
// through its signin.RegistrationPolicy, and a policy insisting on the terms
// being accepted is the seam doing what it was added for — so a registration
// naming none would be refused before the suite asserted anything. Agreements
// are a closed set, so naming every one satisfies any such policy, and a
// deployment requiring none only stamps them, which no assertion reads.
func registrationRequest() *signinpb.RegisterRequest {
	username := "conf_" + identifiers.New()

	return &signinpb.RegisterRequest{
		User: &identitypb.UserRegistrationInput{
			Username:     username,
			EmailAddress: freshEmail(),
			FirstName:    "Some",
		},
		Account:    &identitypb.AccountCreationInput{Name: username + "'s"},
		Agreements: everyAgreement(),
	}
}

// everyAgreement is every document a registrant can accept, read off the enum
// so that a third one is accepted the day it is added.
func everyAgreement() []identitypb.Agreement {
	var agreements []identitypb.Agreement

	for number := range identitypb.Agreement_name {
		if agreement := identitypb.Agreement(number); agreement != identitypb.Agreement_AGREEMENT_UNSPECIFIED {
			agreements = append(agreements, agreement)
		}
	}

	slices.Sort(agreements)

	return agreements
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

// admitsPasswordless skips where the subject's registration policy refuses a
// registrant who names no password, which it declares in
// Seams.PasswordlessRegistrationRefused. Every assertion whose subject is
// somebody with no password calls it before registering one.
func admitsPasswordless(t *testing.T, s *conformance.Session) {
	t.Helper()

	if s.Seams().PasswordlessRegistrationRefused {
		conformance.Skip(t, "conformance: this subject's registration policy refuses a registrant who names no password (Seams.PasswordlessRegistrationRefused), so there is nobody with no password to assert about; skipping")
	}
}

// register registers somebody through sign-in's own door — see
// registrationDoor — and fails the test if the registration is refused.
func register(t *testing.T, s *conformance.Session, request *signinpb.RegisterRequest) (*registrant, *signinpb.Registered) {
	t.Helper()

	response, err := registrationDoor(t, s)(t.Context(), request)
	must.NoError(t, err, must.Sprint("registering somebody through sign-in"))

	registered := response.GetRegistration()
	must.NotNil(t, registered, must.Sprint("a registration answered with no registration"))
	must.NotEqOp(t, "", registered.GetUser().GetId(), must.Sprint("a registration answered with no user"))

	return &registrant{
		userID:    registered.GetUser().GetId(),
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

// loginInto signs in through the password door, naming the account the token
// is to be issued for.
func loginInto(
	ctx context.Context,
	client signinpb.SignInServiceClient,
	username, secret, accountID string,
) (*signinpb.IssuedToken, error) {
	response, err := client.LoginForToken(ctx, &signinpb.LoginForTokenRequest{
		Credentials: &signinpb.Credentials{Username: username, Password: secret, ActiveAccountId: accountID},
	})
	if err != nil {
		return nil, err
	}

	return response.GetToken(), nil
}

// adminLogin signs in through the administrative door.
func adminLogin(
	ctx context.Context,
	client signinpb.SignInServiceClient,
	username, secret, code string,
) (*signinpb.IssuedToken, error) {
	response, err := client.AdminLoginForToken(ctx, &signinpb.AdminLoginForTokenRequest{
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

// unproven is the password door's answer to a registrant whose address no link
// has proven yet: refused with USER_UNVERIFIED, which is what a registration
// with no policy writes, or admitted where the subject declares
// Seams.RegistrantsAdmittedUnverified. It returns the token it admitted them
// with, and nil where it refused them.
func unproven(t *testing.T, s *conformance.Session, anon signinpb.SignInServiceClient, who *registrant) *signinpb.IssuedToken {
	t.Helper()

	issued, err := login(t.Context(), anon, who.username, password, "")
	if !s.Seams().RegistrantsAdmittedUnverified {
		refused(t, s, err, codes.FailedPrecondition, reasonUserUnverified)

		return nil
	}

	must.NoError(t, err, must.Sprint("this subject says its registrants are admitted before their address is proven (Seams.RegistrantsAdmittedUnverified), and one was refused"))
	must.NotNil(t, issued, must.Sprint("a sign-in answered with no token"))

	return issued
}

// addressProven reports whether the holder of issued is told their address is
// proven, asked as them. Where the door admitted them unverified that is the
// only way to observe that a link moved their standing, since the door's
// answer does not change. ok is false where the subject supplies no SignedIn
// seam to ask through.
func addressProven(t *testing.T, s *conformance.Session, issued *signinpb.IssuedToken) (proven, ok bool) {
	t.Helper()

	if s.Seams().SignedIn == nil {
		return false, false
	}

	sub := caller(t, s, issued, getAuthStatus)

	response, err := sub.Surfaces.SignIn.GetAuthStatus(sub.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
	must.NoError(t, err, must.Sprint("reading a registrant's own standing"))

	return response.GetStatus().GetEmailAddressVerified(), true
}

// caller is who a token the suite signed in for calls as, making methods: the
// seam that turns an issued token back into a caller, over a connection held to
// the calls it declares. It skips where the subject supplies no such seam.
func caller(t *testing.T, s *conformance.Session, issued *signinpb.IssuedToken, methods ...string) *conformance.Subject {
	t.Helper()

	return s.SignedIn(t, issued, conformance.Making(methods...), conformance.InTenant(surface, tenancy.Global()))
}

// signedIn is somebody registered with a password, verified and signed in, and
// the caller their token makes, making methods.
//
// A registrant rather than a minted caller, so every credential they hold is
// one the suite chose: the password it typed, and no second factor until an
// assertion enrolls one. That is what lets an assertion reauthenticate as them
// against any deployment, including one whose minted callers hold a proven
// factor the suite cannot read. anon must be able to make verifyEmailAddress
// and loginForToken.
func signedIn(
	t *testing.T,
	s *conformance.Session,
	anon signinpb.SignInServiceClient,
	methods ...string,
) (*conformance.Subject, *registrant) {
	t.Helper()

	if s.Seams().SignedIn == nil {
		conformance.Skip(t, "conformance: this subject supplies no SignedIn seam, so nobody the suite signs in can be called as; skipping")
	}

	who := signInAs(t, s, anon)

	return caller(t, s, loggedIn(t, anon, who.username, password), methods...), who
}

// directoryCaller is a caller in the global directory — the one a registrant
// is in — making identity's methods, for the states a registrant cannot bring
// about on their own: an invitation into somebody else's account, a role or a
// forced change only somebody with standing over them can impose. It skips
// where the subject mounts no identity surface.
func directoryCaller(t *testing.T, s *conformance.Session, methods ...string) *conformance.Subject {
	t.Helper()

	sub := s.Subject(t, conformance.Making(methods...), conformance.InTenant(identitySurface, tenancy.Global()))
	if sub.Surfaces.Identity == nil {
		conformance.Skip(t, "conformance: this subject mounts no identity surface, so nobody can act on a registrant from the directory; skipping")
	}

	return sub
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
