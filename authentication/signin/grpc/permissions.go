package grpc

import (
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// This package's authorization fragment: SignInService's, which is a list of
// names rather than a map of permissions, and SignInAdministrationService's,
// which is a map.
//
// Nothing on SignInService is permissioned, and that is a conclusion rather
// than an omission. The anonymous RPCs are how a caller becomes somebody at all — or,
// in SignOut's case, stops being them — so there is no grant that could gate
// them: a permission check in front of sign-in is a check against the caller's
// roles, and an anonymous caller has none. Register is among them, and is the
// door a caller becomes somebody through for the first time; who may use it is
// the deployment's signin.RegistrationPolicy, which the service runs on every
// registration, and a permission here would be this module inventing a grant
// for a decision it does not make. The rest take their subject from the
// principal and have no field that could name anybody else — the whole of their
// authorization is "this is the caller's own row", checked by the method having
// no way to be about another one. EndSignIn names a login rather than a person,
// and is among them because the caller is part of what it matches: a family
// that is not theirs ends nothing.
//
// So SignInService has no entry in [Permissions], unlike identity/grpc's
// service, and a consumer looking for one is looking for something that would
// be wrong to have. What there is instead is [Require], which declares every
// one of them to an authorization policy explicitly. The difference between "declared and
// requires nothing" and "not declared" is the difference between a service that
// works and one whose every method is denied by the enforcer's fail-closed
// rule, and nothing reports the second at wiring time — which is exactly why
// the declaration is a function here rather than a paragraph telling a consumer
// to write a loop.
//
// What an operator does to somebody else's logins is a different act, and it
// is SignInAdministrationService rather than a field on SignInService naming a
// user — a field that would turn every "this is the caller's own row" above
// into a question about the caller's grants. Being a service of its own is what
// keeps that sentence true of this one.

// The permissions SignInAdministrationService's methods require, in
// authorization's vocabulary, spelled beside the service for the reason
// identity/grpc gives for its own: the strings mean something only to sign-in.
//
// No role holds either by default, and nothing here says who should. Which of
// a deployment's callers are operators is its policy's to decide, and these
// are the names that policy grants.
const (
	// PermissionReadAnySignIns covers listing somebody else's live logins.
	//
	// It is its own grant, apart from ending them, because a support desk
	// answering "am I signed in somewhere I shouldn't be?" needs to read the
	// list and not thereby to end anything on it.
	PermissionReadAnySignIns authorization.Permission = "signin.sign_ins.read_any"

	// PermissionEndAnySignIns covers ending somebody else's logins: one by
	// its family, or every one they hold.
	//
	// One grant for both sizes, because the second is the first repeated over
	// the list, and a role that could end each login and not all of them
	// would be refused nothing it could not do a row at a time.
	PermissionEndAnySignIns authorization.Permission = "signin.sign_ins.end_any"
)

// Permissions is the default map from SignInAdministrationService's methods to
// what each requires, the fragment [Require] declares.
//
// Every method on that service is here and no method on SignInService is:
// those are [AnonymousMethods] and [SelfServiceMethods], and the three
// together are exhaustive over what [Server] serves.
// permissions_test.go keeps them so.
//
// It is a default and not a rule, as identity/grpc's is: a consumer who wants
// the listing behind two permissions declares the whole fragment themselves.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		signinpb.SignInAdministrationService_ListSignInsForUser_FullMethodName:   {PermissionReadAnySignIns},
		signinpb.SignInAdministrationService_EndSignInForUser_FullMethodName:     {PermissionEndAnySignIns},
		signinpb.SignInAdministrationService_EndAllSignInsForUser_FullMethodName: {PermissionEndAnySignIns},
	}
}

// AnonymousMethods are the RPCs that require no caller at all.
//
// The first is Register, the sign-up door, and it is open by default. What an
// open sign-up has policy about — who may register, which agreements they must
// accept, what standing and roles they start with — is the deployment's
// signin.RegistrationPolicy, which the service runs on every registration
// before anything is hashed, minted or written; the roles a registrant owns
// their account with are the service's default owner roles or what that policy
// replaced them with, never the request's. A caller who is signed in still
// reaches it — [RequireAuthentication] declares it optional rather than
// anonymous, so an operator provisioning users is resolved and the policy can
// read them off the context — but nobody is required. What is left in front of
// it is the rate it is called at, which is the consumer's to bound, as it is for
// the sign-in doors. A deployment that does not want sign-up says so with
// [WithoutOpenRegistration]; the method stays in this list either way, because
// the lists are fixed and the server is the one place that decides.
//
// Two more are the doors and a third is ExchangeRefreshToken, which is a door
// as well: the credential it presents is the whole of its authority, and a caller
// holding one has not been authenticated yet. Requiring a principal there would
// require a live access token to renew an expired one, which is the one moment a
// client has none.
//
// SwitchAccount is ExchangeRefreshToken with an account named, and anonymous
// for the same reason: the refresh token it presents is the whole of its
// authority, and moving to another workspace is something a client does at the
// moment its access token may well have expired.
//
// GetAuthStatus is anonymous too, and it answers "no" rather than refusing —
// see its own documentation for why a whoami that refuses anonymous callers
// makes every client treat its first question as an error.
//
// The next two are the doors that finish a registration, and they are anonymous
// for the reason the first three are: the token mailed to the person they are
// about is the whole of the request's authority, and requiring a principal would
// require a sign-in from somebody who cannot sign in yet — which is the dead end
// both of them exist to open. Neither has a field naming a user; who they are
// about is read off the row the token named.
//
// The next two are the passwordless door, and they are the same argument twice
// over. Redeeming carries a mailed token exactly as those two do. Requesting
// carries nothing at all — it names an address and is answered identically
// whoever holds it — so there is no principal it could require and nothing a
// permission could protect: the thing that must not be abused there is the rate
// it is called at, which is the consumer's to bound in front of it.
//
// RequestHandleReminder is the request half of that argument again: it names an
// address, is answered identically whoever holds it, and is for somebody who
// cannot sign in because they have forgotten what they sign in as.
//
// The last is SignOut, which is ExchangeRefreshToken's argument read backwards.
// It presents the same credential and it is the moment a client is least likely
// to hold a live access token: an application closed for a week has an expired
// one, and a sign-out that required it would refuse everybody who had waited
// long enough to want it.
func AnonymousMethods() []string {
	return []string{
		signinpb.SignInService_Register_FullMethodName,
		signinpb.SignInService_LoginForToken_FullMethodName,
		signinpb.SignInService_AdminLoginForToken_FullMethodName,
		signinpb.SignInService_ExchangeRefreshToken_FullMethodName,
		signinpb.SignInService_SwitchAccount_FullMethodName,
		signinpb.SignInService_GetAuthStatus_FullMethodName,
		signinpb.SignInService_AttachPassword_FullMethodName,
		signinpb.SignInService_VerifyEmailAddress_FullMethodName,
		signinpb.SignInService_RequestMagicLink_FullMethodName,
		signinpb.SignInService_RedeemMagicLink_FullMethodName,
		signinpb.SignInService_RequestVerificationEmailByAddress_FullMethodName,
		signinpb.SignInService_RequestHandleReminder_FullMethodName,
		signinpb.SignInService_SignOut_FullMethodName,
	}
}

// SelfServiceMethods are the RPCs that require a caller and are always about
// that caller.
//
// Every one takes its subject from the principal. There is no permission that
// would make these safer and one would make them wrong: an operator holding a
// directory-wide grant would not thereby be able to change somebody else's
// password — or list or end their logins, or mail them a verification link —
// because the method has no way to name one. EndSignIn names a login, and the service matches it against the
// caller as well, so a family identifier that is somebody else's ends nothing.
// EndOtherSignIns names nothing at all: the login it keeps is read off the
// caller's own token.
func SelfServiceMethods() []string {
	return []string{
		signinpb.SignInService_GetSelf_FullMethodName,
		signinpb.SignInService_UpdatePassword_FullMethodName,
		signinpb.SignInService_RefreshTOTPSecret_FullMethodName,
		signinpb.SignInService_VerifyTOTPSecret_FullMethodName,
		signinpb.SignInService_UpdateEmailAddress_FullMethodName,
		signinpb.SignInService_UpdateUsername_FullMethodName,
		signinpb.SignInService_SignOutEverywhere_FullMethodName,
		signinpb.SignInService_ListSignIns_FullMethodName,
		signinpb.SignInService_EndSignIn_FullMethodName,
		signinpb.SignInService_EndOtherSignIns_FullMethodName,
		signinpb.SignInService_RequestVerificationEmail_FullMethodName,
	}
}

// Require declares every one of this package's methods onto a requirements
// builder: SignInService's as public, and SignInAdministrationService's with
// what [Permissions] says they need.
//
// Public there means "no authorization check", not "no authentication": the
// consumer's authentication interceptor still runs, and the self-service
// methods refuse a request with no principal on them. The anonymous ones are
// the service working as intended.
//
// It takes and returns the builder rather than building it, so a consumer
// composes several domains and their own methods into one table:
//
//	reqs, err := signingrpc.Require(identitygrpc.Require(authzgrpc.NewRequirements())).
//		RequireAll(newsletters.Permissions()).
//		Public(healthpb.Health_Check_FullMethodName).
//		Build()
//
// A method declared twice is ErrDuplicateMethod, so a consumer who wants one of
// these gated after all declares the whole set themselves rather than calling
// this and amending it. That is the same bargain identity/grpc's Require makes,
// and it is why this exists at all: the two lists and the map above are
// exhaustive over both services, permissions_test.go keeps them that way, and an RPC added later
// and decided about in none of them fails there rather than being denied in
// somebody's production.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	b.RequireAll(Permissions())

	for _, method := range AnonymousMethods() {
		b.Public(method)
	}

	for _, method := range SelfServiceMethods() {
		b.Public(method)
	}

	return b
}
