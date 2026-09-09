package grpc_test

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/audit/auditpb"
	auditgrpc "github.com/primandproper/platform-go/v14/audit/grpc"

	"github.com/primandproper/primitives-go/authorization"
	authzgrpc "github.com/primandproper/primitives-go/authorization/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// serviceMethods is every RPC the generated service descriptor declares, in the
// full-method form an interceptor sees.
//
// Reading it off the descriptor rather than listing it here is what makes this
// file a check rather than a second copy: an RPC added to the schema appears
// here without anybody remembering to add it.
func serviceMethods() []string {
	prefix := "/" + auditpb.AuditService_ServiceDesc.ServiceName + "/"

	out := make([]string, 0, len(auditpb.AuditService_ServiceDesc.Methods))
	for _, m := range auditpb.AuditService_ServiceDesc.Methods {
		out = append(out, prefix+m.MethodName)
	}

	return out
}

// TestEveryMethodIsPermissioned is the property the map exists for. There are no
// public methods on this service and no self-service ones — a log has no notion
// of a caller's own row — so an RPC added later and left out of Permissions is
// one the enforcer denies in somebody's production, for a reason nothing
// connects to a missing entry.
func TestEveryMethodIsPermissioned(T *testing.T) {
	T.Parallel()

	permissions := auditgrpc.Permissions()

	for _, method := range serviceMethods() {
		required, ok := permissions[method]
		test.True(T, ok, test.Sprintf("%s requires nothing, so nothing says who may call it", method))
		test.SliceNotEmpty(T, required, test.Sprintf("%s is declared with no permission at all", method))
	}
}

// TestPermissionsNameOnlyRealMethods is the other direction, and the one a
// rename breaks. A key naming a method that no longer exists reads exactly like
// a live one, and a builder cannot tell it from a method deliberately absent.
func TestPermissionsNameOnlyRealMethods(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()

	for method := range auditgrpc.Permissions() {
		test.SliceContains(T, methods, method, test.Sprintf(
			"%s is named in Permissions and is not an RPC on this service", method))
	}
}

// TestVerifyIsItsOwnGrant is the decision this file is here to keep. A
// verification carries no entry's content, so the monitor that runs one on a
// schedule need not also be able to read what everybody did — and a permission
// set that collapsed the two would make an alerting job a reader of the audit
// log.
func TestVerifyIsItsOwnGrant(T *testing.T) {
	T.Parallel()

	permissions := auditgrpc.Permissions()

	verify := permissions[auditpb.AuditService_VerifyChain_FullMethodName]
	must.SliceContains(T, verify, auditgrpc.PermissionVerifyChain)
	test.SliceNotContains(T, verify, auditgrpc.PermissionReadEntries)

	for _, method := range []string{
		auditpb.AuditService_GetEntry_FullMethodName,
		auditpb.AuditService_ListEntries_FullMethodName,
	} {
		test.SliceNotContains(T, permissions[method], auditgrpc.PermissionVerifyChain, test.Sprintf(
			"%s requires the verification grant, which reads nothing it needs", method))
	}
}

func TestRequire(T *testing.T) {
	T.Parallel()

	T.Run("declares every method with what it needs", func(t *testing.T) {
		t.Parallel()

		reqs, err := auditgrpc.Require(authzgrpc.NewRequirements()).Build()
		must.NoError(t, err)

		// Every one, and nothing else. A method this service serves and does
		// not declare is denied by the enforcer's fail-closed rule, and one
		// declared that it does not serve is a string nothing will ever match.
		declared := reqs.Methods()
		test.SliceLen(t, len(serviceMethods()), declared)

		for _, method := range serviceMethods() {
			test.SliceContains(t, declared, method)
		}
	})

	// The builder refuses a method declared twice, so a consumer overriding one
	// of these builds the map themselves rather than calling this and amending
	// it. Asserting it here is what keeps that sentence in the doc true.
	T.Run("refuses to declare a method twice", func(t *testing.T) {
		t.Parallel()

		_, err := auditgrpc.Require(auditgrpc.Require(authzgrpc.NewRequirements())).Build()
		test.Error(t, err)
	})

	T.Run("passes a nil builder through", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, auditgrpc.Require(nil))
	})
}

// TestPermissionsAreNamespaced keeps the two names from colliding with another
// domain's fragment in a consumer's composed policy.
func TestPermissionsAreNamespaced(T *testing.T) {
	T.Parallel()

	for _, permission := range []authorization.Permission{
		auditgrpc.PermissionReadEntries,
		auditgrpc.PermissionVerifyChain,
	} {
		test.StrHasPrefix(T, "audit.", string(permission))
	}

	test.False(T, slices.Contains(
		[]authorization.Permission{auditgrpc.PermissionReadEntries},
		auditgrpc.PermissionVerifyChain))
}
