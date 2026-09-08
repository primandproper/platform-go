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

// TestEveryMethodIsPermissioned is the reason this file exists, and it is the
// decision this service made rather than a property it happens to have.
//
// There is no second set of methods here that require nothing. An earlier
// revision served five that did — a self-service mirror, reachable behind no
// permission on the theory that owning the row is the authorization — and they
// were removed because no consumer called them. So the invariant is now the
// simplest one available: every RPC the descriptor declares is in Permissions.
//
// It fails in both of the directions that matter. An RPC added later and left
// out of the map is a method a consumer's fail-closed enforcer denies in
// production, for a reason nobody would connect to this package; and an RPC
// named *Own* is the permissionless half coming back without the argument for it
// coming back too — the .proto says what that argument would have to be.
func TestEveryMethodIsPermissioned(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("no methods on the service descriptor, so this asserted nothing"))

	permissions := oauth2clientsgrpc.Permissions()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, permissioned := permissions[method]
			test.True(t, permissioned, test.Sprintf(
				"%s is not in Permissions, so nothing says who may call it", method))

			test.False(t, strings.Contains(method, "Own"), test.Sprintf(
				"%s reads as a self-service method, which this service does not serve", method))
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

// TestRequireDeclaresEveryMethod is the check a consumer writing the RequireAll
// themselves would not have. authorization/grpc is fail-closed, so a method
// Require left out is denied, and a denial for want of a declaration looks
// exactly like a policy somebody meant.
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
