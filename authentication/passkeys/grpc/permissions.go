package grpc

import (
	"github.com/primandproper/platform-go/v14/authentication/passkeys/passkeyspb"
	"github.com/primandproper/platform-go/v14/rbac"

	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// This service's authorization fragment. Nothing here is permissioned, for
// the reasons authentication/signin/grpc gives of its own: the login half is
// how a caller becomes somebody, so an anonymous caller has no roles for a
// permission to check, and the self-service half takes its subject from the
// principal and has no field that could name anybody else.
//
// A deployment that wants any of these reserved — enrollment to operators
// during a rollout, say — reserves it at its own authorization table; nothing
// here decides that for it.

// AnonymousMethods are the RPCs that require no caller: the two halves of a
// login. The assertion the second carries is the whole of its authority, and
// requiring a principal would require somebody to be signed in before they
// could sign in.
func AnonymousMethods() []string {
	return []string{
		passkeyspb.PasskeysService_BeginLogin_FullMethodName,
		passkeyspb.PasskeysService_FinishLogin_FullMethodName,
	}
}

// SelfServiceMethods are the RPCs that require a caller and are always about
// that caller.
//
// Each takes its subject from the principal. An operator holding a
// directory-wide grant still could not enroll a passkey on somebody else's
// account, list theirs or archive one through these, because no method has a
// way to name anybody. ArchivePasskey names a passkey, and the service matches
// the caller as well, so one that is somebody else's is not found.
func SelfServiceMethods() []string {
	return []string{
		passkeyspb.PasskeysService_BeginRegistration_FullMethodName,
		passkeyspb.PasskeysService_FinishRegistration_FullMethodName,
		passkeyspb.PasskeysService_ListPasskeys_FullMethodName,
		passkeyspb.PasskeysService_ArchivePasskey_FullMethodName,
	}
}

// Require declares every one of this service's methods onto a requirements
// builder, all of them as public — "no authorization check", not "no
// authentication": the self-service methods still refuse a request with no
// principal on it.
//
// It takes and returns the builder, so a consumer composes it with the other
// surfaces' fragments into one table. A method declared twice is
// ErrDuplicateMethod, the bargain authentication/signin/grpc's Require makes.
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

// Tiers is this service's share of a deployment's policy, which is nothing:
// every method is [AnonymousMethods] or [SelfServiceMethods], and none holds a
// permission. It is exported so a deployment composing every surface's tiers
// with rbac.MergeTiers names this one too, and picks up whatever it grows
// without a change of its own.
func Tiers() rbac.Tiers {
	return rbac.Tiers{}
}
