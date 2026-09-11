package grpc

import (
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// This service's authorization fragment, and the reason it is a list of names
// rather than a map of permissions.
//
// Nothing here is permissioned, and that is a conclusion rather than an
// omission. Three of the seven RPCs are how a caller becomes somebody at all,
// so there is no grant that could gate them: a permission check in front of
// sign-in is a check against the caller's roles, and an anonymous caller has
// none. The other four take their subject from the principal and have no field
// that could name anybody else — the whole of their authorization is "this is
// the caller's own row", checked by the method having no way to be about
// another one.
//
// So there is no Permissions map here, unlike identity/grpc, and a consumer
// looking for one is looking for something that would be wrong to have. What
// there is instead is [Require], which declares all seven to an authorization
// policy explicitly. The difference between "declared and requires nothing" and
// "not declared" is the difference between a service that works and one whose
// every method is denied by the enforcer's fail-closed rule, and nothing reports
// the second at wiring time — which is exactly why the declaration is a function
// here rather than a paragraph telling a consumer to write a loop.

// AnonymousMethods are the RPCs that require no caller at all.
//
// Two of them are the doors, and the third is GetAuthStatus, which answers "no"
// rather than refusing — see its own documentation for why a whoami that
// refuses anonymous callers makes every client treat its first question as an
// error.
func AnonymousMethods() []string {
	return []string{
		signinpb.SignInService_LoginForToken_FullMethodName,
		signinpb.SignInService_AdminLoginForToken_FullMethodName,
		signinpb.SignInService_GetAuthStatus_FullMethodName,
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
// methods refuse a request with no principal on them. The three anonymous ones
// are the service working as intended.
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
// and it is why this exists at all: the two lists above are exhaustive over the
// service, permissions_test.go keeps them that way, and an RPC added later and
// decided about in neither fails there rather than being denied in somebody's
// production.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	for _, method := range AnonymousMethods() {
		b.Public(method)
	}

	for _, method := range SelfServiceMethods() {
		b.Public(method)
	}

	return b
}
