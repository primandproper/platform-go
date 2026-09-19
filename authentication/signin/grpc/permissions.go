package grpc

import (
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// This service's authorization fragment, and the reason it is a list of names
// rather than a map of permissions.
//
// Nothing here is permissioned, and that is a conclusion rather than an
// omission. Six of the eleven RPCs are how a caller becomes somebody at all, so
// there is no grant that could gate them: a permission check in front of
// sign-in is a check against the caller's roles, and an anonymous caller has
// none. Four take their subject from the principal and have no field that could
// name anybody else — the whole of their authorization is "this is the caller's
// own row", checked by the method having no way to be about another one.
//
// The eleventh is Register, which is neither: it requires a caller and is not
// about them. It is still ungated, and that is the same conclusion identity's
// namesake reaches — the registrar is the consumer's own service, the policy
// that decides who may sign up is theirs and sits in front of the call, and a
// permission here would be this module inventing a grant for a decision it does
// not make. What it is not is anonymous, which is why it is a list of its own
// rather than an entry in either of the two below.
//
// So there is no Permissions map here, unlike identity/grpc, and a consumer
// looking for one is looking for something that would be wrong to have. What
// there is instead is [Require], which declares all eleven to an authorization
// policy explicitly. The difference between "declared and requires nothing" and
// "not declared" is the difference between a service that works and one whose
// every method is denied by the enforcer's fail-closed rule, and nothing reports
// the second at wiring time — which is exactly why the declaration is a function
// here rather than a paragraph telling a consumer to write a loop.

// AnonymousMethods are the RPCs that require no caller at all.
//
// Two of them are the doors and a third is ExchangeRefreshToken, which is a door
// as well: the credential it presents is the whole of its authority, and a caller
// holding one has not been authenticated yet. Requiring a principal there would
// require a live access token to renew an expired one, which is the one moment a
// client has none.
//
// The fourth is GetAuthStatus, which answers "no" rather than refusing — see its
// own documentation for why a whoami that refuses anonymous callers makes every
// client treat its first question as an error.
//
// The last two are the doors that finish a registration, and they are anonymous
// for the reason the first three are: the token mailed to the person they are
// about is the whole of the request's authority, and requiring a principal would
// require a sign-in from somebody who cannot sign in yet — which is the dead end
// both of them exist to open. Neither has a field naming a user; who they are
// about is read off the row the token named.
func AnonymousMethods() []string {
	return []string{
		signinpb.SignInService_LoginForToken_FullMethodName,
		signinpb.SignInService_AdminLoginForToken_FullMethodName,
		signinpb.SignInService_ExchangeRefreshToken_FullMethodName,
		signinpb.SignInService_GetAuthStatus_FullMethodName,
		signinpb.SignInService_AttachPassword_FullMethodName,
		signinpb.SignInService_VerifyEmailAddress_FullMethodName,
	}
}

// RegistrarMethods are the RPCs that require a caller and are not about that
// caller.
//
// There is one, and the list exists rather than the method being folded into
// SelfServiceMethods because the difference is the thing a reader of this file
// most needs to see: every other authenticated RPC here is safe by having no way
// to name anybody but the caller, and this one is safe because the consumer's
// own registration policy stands in front of it. Collapsing the two would make
// the first sentence of that pair untrue of the list that states it.
func RegistrarMethods() []string {
	return []string{
		signinpb.SignInService_Register_FullMethodName,
	}
}

// SelfServiceMethods are the RPCs that require a caller and are always about
// that caller.
//
// Every one takes its subject from the principal. There is no permission that
// would make these safer and one would make them wrong: an operator holding a
// directory-wide grant would not thereby be able to change somebody else's
// password, because the method has no way to name one.
func SelfServiceMethods() []string {
	return []string{
		signinpb.SignInService_GetSelf_FullMethodName,
		signinpb.SignInService_UpdatePassword_FullMethodName,
		signinpb.SignInService_RefreshTOTPSecret_FullMethodName,
		signinpb.SignInService_VerifyTOTPSecret_FullMethodName,
	}
}

// Require declares every one of this service's methods onto a requirements
// builder, all of them as public.
//
// Public there means "no authorization check", not "no authentication": the
// consumer's authentication interceptor still runs, and the four self-service
// methods and Register refuse a request with no principal on them. The six
// anonymous ones are the service working as intended.
//
// It takes and returns the builder rather than building it, so a consumer
// composes several domains and their own methods into one table:
//
//	reqs, err := signingrpc.Require(identitygrpc.Require(authzgrpc.NewRequirements())).
//		RequireAll(mealplanning.Permissions()).
//		Public(healthpb.Health_Check_FullMethodName).
//		Build()
//
// A method declared twice is ErrDuplicateMethod, so a consumer who wants one of
// these gated after all declares the whole set themselves rather than calling
// this and amending it. That is the same bargain identity/grpc's Require makes,
// and it is why this exists at all: the three lists above are exhaustive over
// the service, permissions_test.go keeps them that way, and an RPC added later
// and decided about in none of them fails there rather than being denied in
// somebody's production.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	for _, method := range AnonymousMethods() {
		b.Public(method)
	}

	for _, method := range RegistrarMethods() {
		b.Public(method)
	}

	for _, method := range SelfServiceMethods() {
		b.Public(method)
	}

	return b
}
