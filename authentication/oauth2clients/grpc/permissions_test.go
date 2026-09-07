package grpc_test

import (
	"slices"
	"strings"
	"testing"

	oauth2clientsgrpc "github.com/primandproper/platform-go/v14/authentication/oauth2clients/grpc"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	authzgrpc "github.com/primandproper/platform-go/v14/authorization/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// serviceMethods is every RPC the generated service descriptor declares, as the
// full method names the interceptor matches on.
//
// It is read off the descriptor rather than written down, which is the whole
// point: a list here would be a third place to forget an RPC, and the tests
// below exist because the first two are easy to forget.
func serviceMethods() []string {
	prefix := "/" + oauth2clientspb.OAuth2ClientsService_ServiceDesc.ServiceName + "/"

	out := make([]string, 0, len(oauth2clientspb.OAuth2ClientsService_ServiceDesc.Methods))
	for _, m := range oauth2clientspb.OAuth2ClientsService_ServiceDesc.Methods {
		out = append(out, prefix+m.MethodName)
	}

	return out
}

// TestEveryMethodHasADecision is the reason this file exists. An RPC added to
// the service later and named in neither Permissions nor SelfServiceMethods has
// had no decision made about it, and the way a consumer would find out is
// authorization/grpc denying it in production — correctly, and for a reason
// nobody would connect to this package.
func TestEveryMethodHasADecision(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("no methods on the service descriptor, so this asserted nothing"))

	permissions := oauth2clientsgrpc.Permissions()
	selfService := oauth2clientsgrpc.SelfServiceMethods()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, permissioned := permissions[method]
			self := slices.Contains(selfService, method)

			test.True(t, permissioned || self, test.Sprintf(
				"%s is in neither Permissions nor SelfServiceMethods, so nothing says who may call it", method))
			test.False(t, permissioned && self, test.Sprintf(
				"%s is in both Permissions and SelfServiceMethods, and authorization/grpc refuses a method declared twice", method))
		})
	}
}

// TestNoDecisionOutlivesItsMethod is the other direction: a decision naming an
// RPC that no longer exists is a permission a consumer is still granting for
// nothing, and a rename would leave the real method undeclared and therefore
// denied.
func TestNoDecisionOutlivesItsMethod(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()

	for method := range oauth2clientsgrpc.Permissions() {
		test.True(T, slices.Contains(methods, method), test.Sprintf(
			"Permissions names %s, which the service descriptor does not declare", method))
	}

	for _, method := range oauth2clientsgrpc.SelfServiceMethods() {
		test.True(T, slices.Contains(methods, method), test.Sprintf(
			"SelfServiceMethods names %s, which the service descriptor does not declare", method))
	}
}

// TestTheSelfServiceHalfIsExactlyTheOwnMethods pins the split this service's
// whole authorization story rests on.
//
// The naming is the contract: an RPC whose name says "Own" reads its owner off
// the principal and can name nobody else's row, and one whose name does not is
// behind a grant. A method added later that got the naming right and the
// declaration wrong — or the other way round — is a permission gap that reads
// as correct in both files separately.
func TestTheSelfServiceHalfIsExactlyTheOwnMethods(T *testing.T) {
	T.Parallel()

	selfService := oauth2clientsgrpc.SelfServiceMethods()
	permissions := oauth2clientsgrpc.Permissions()

	for _, method := range serviceMethods() {
		own := strings.Contains(method, "Own")

		T.Run(method, func(t *testing.T) {
			t.Parallel()

			if own {
				test.True(t, slices.Contains(selfService, method), test.Sprintf(
					"%s names an owned registration and is not declared self-service", method))

				return
			}

			_, permissioned := permissions[method]
			test.True(t, permissioned, test.Sprintf(
				"%s acts on any registration in the registry and requires no permission", method))
		})
	}
}

// TestNoPermissionIsEmpty catches the decision that looks made and is not: an
// entry mapping a method to an empty slice declares it and requires nothing.
func TestNoPermissionIsEmpty(T *testing.T) {
	T.Parallel()

	for method, permissions := range oauth2clientsgrpc.Permissions() {
		test.SliceNotEmpty(T, permissions, test.Sprintf(
			"%s is declared with no permissions, which grants it to everybody", method))
	}
}

// TestPermissionsReturnsAFreshMap keeps a consumer's override from editing every
// other consumer's copy in the same process.
func TestPermissionsReturnsAFreshMap(T *testing.T) {
	T.Parallel()

	first := oauth2clientsgrpc.Permissions()
	for method := range first {
		delete(first, method)
	}

	test.MapNotEmpty(T, oauth2clientsgrpc.Permissions(),
		test.Sprint("Permissions handed back a map a caller could empty for everybody"))
}

// TestRequireDeclaresEveryMethod is the check the two-step version cannot make
// of itself. authorization/grpc is fail-closed, so a method Require left out is
// denied — and a self-service method denied for want of a Public declaration
// looks exactly like a policy somebody meant.
func TestRequireDeclaresEveryMethod(T *testing.T) {
	T.Parallel()

	reqs, err := oauth2clientsgrpc.Require(authzgrpc.NewRequirements()).Build()
	must.NoError(T, err)

	declared := reqs.Methods()

	for _, method := range serviceMethods() {
		test.SliceContains(T, declared, method, test.Sprintf(
			"%s is not declared after Require, so the enforcer will deny it as undeclared", method))
	}
}

// TestRequireToleratesANilBuilder keeps a chain composing several domains'
// fragments from needing a nil check per link.
func TestRequireToleratesANilBuilder(T *testing.T) {
	T.Parallel()

	test.Nil(T, oauth2clientsgrpc.Require(nil))
}
