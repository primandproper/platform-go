package grpc

import (
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"

	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// AnonymousMethods are this service's RPCs, all three of them.
//
// There is no second list, and that is the whole authorization story here: a
// caller who could prove who they are would be changing their password through
// sign-in instead, so there is no principal any of these could require and no
// grant a permission could check. A roster of one kind is still a roster,
// because permissions_test.go holds it against the service descriptor — an RPC
// added later and left out of it fails there rather than being denied in
// somebody's production.
func AnonymousMethods() []string {
	return []string{
		passwordresetpb.PasswordResetService_RequestPasswordReset_FullMethodName,
		passwordresetpb.PasswordResetService_VerifyPasswordResetToken_FullMethodName,
		passwordresetpb.PasswordResetService_CompletePasswordReset_FullMethodName,
	}
}

// Require declares every one of this service's methods onto a requirements
// builder, all of them as public.
//
// Public there means "no authorization check", and here it also means no
// authentication: nothing on this service reads a caller, so a request with no
// principal is the service working as intended rather than a request that will
// be refused one layer in.
//
// It takes and returns the builder rather than building it, so a consumer
// composes several domains and their own methods into one table:
//
//	reqs, err := passwordresetgrpc.Require(signingrpc.Require(identitygrpc.Require(authzgrpc.NewRequirements()))).
//		Build()
//
// Declaring them matters more here than anywhere else in this module. An
// enforcer's fail-closed rule denies what it has not been told about, and what it
// would be denying is the only way back in for somebody who cannot sign in —
// which is a lockout nothing reports at wiring time.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	for _, method := range AnonymousMethods() {
		b.Public(method)
	}

	return b
}
