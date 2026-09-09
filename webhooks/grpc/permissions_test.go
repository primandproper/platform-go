package grpc_test

import (
	"slices"
	"testing"

	webhooksgrpc "github.com/primandproper/platform-go/v14/webhooks/grpc"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	authzgrpc "github.com/primandproper/primitives-go/authorization/grpc"

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
	prefix := "/" + webhookspb.WebhooksService_ServiceDesc.ServiceName + "/"

	out := make([]string, 0, len(webhookspb.WebhooksService_ServiceDesc.Methods))
	for _, m := range webhookspb.WebhooksService_ServiceDesc.Methods {
		out = append(out, prefix+m.MethodName)
	}

	return out
}

// TestEveryMethodIsPermissioned is the reason this file exists, and it is the
// decision this service made rather than a property it happens to have.
//
// There is no second set of methods here that require nothing. Every row this
// surface reaches is somebody's configuration or somebody's delivery history,
// and neither is a thing an unauthorized caller reads because they happened to
// know an identifier.
func TestEveryMethodIsPermissioned(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("no methods on the service descriptor, so this asserted nothing"))

	permissions := webhooksgrpc.Permissions()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, permissioned := permissions[method]
			test.True(t, permissioned, test.Sprintf(
				"%s is not in Permissions, so nothing says who may call it", method))
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

	for method := range webhooksgrpc.Permissions() {
		test.True(T, slices.Contains(methods, method), test.Sprintf(
			"Permissions names %s, which the service descriptor does not declare", method))
	}
}

// TestNoPermissionIsEmpty catches the decision that looks made and is not: an
// entry mapping a method to an empty slice declares it and requires nothing.
func TestNoPermissionIsEmpty(T *testing.T) {
	T.Parallel()

	for method, permissions := range webhooksgrpc.Permissions() {
		test.SliceNotEmpty(T, permissions, test.Sprintf(
			"%s is declared with no permissions, which grants it to everybody", method))
	}
}

// TestTheWriteGrantsAreDistinct is the split subscriptions being rows bought.
//
// Pointing a tenant's event stream somewhere else and signing it in the
// deployment's name is a different amount of trust from letting a team pick
// which events their existing endpoint receives, and against a flat list of
// event types on the endpoint the two would have to be one grant.
func TestTheWriteGrantsAreDistinct(T *testing.T) {
	T.Parallel()

	permissions := webhooksgrpc.Permissions()

	save := permissions[webhookspb.WebhooksService_SaveEndpoint_FullMethodName]
	add := permissions[webhookspb.WebhooksService_AddSubscription_FullMethodName]

	must.SliceNotEmpty(T, save)
	must.SliceNotEmpty(T, add)

	test.False(T, slices.Contains(add, webhooksgrpc.PermissionSaveEndpoints), test.Sprint(
		"adding a subscription requires the grant that lets a caller redirect the stream"))

	// And the delivery log is not reachable with the grant that reads
	// configuration: one says where a tenant's events go, the other carries a
	// subscriber's own error text.
	test.False(T, slices.Contains(
		permissions[webhookspb.WebhooksService_ListAttempts_FullMethodName],
		webhooksgrpc.PermissionReadEndpoints,
	))
}

// TestPermissionsReturnsAFreshMap keeps a consumer's override from editing every
// other consumer's copy in the same process.
func TestPermissionsReturnsAFreshMap(T *testing.T) {
	T.Parallel()

	first := webhooksgrpc.Permissions()
	for method := range first {
		delete(first, method)
	}

	test.MapNotEmpty(T, webhooksgrpc.Permissions(),
		test.Sprint("Permissions handed back a map a caller could empty for everybody"))
}

// TestRequireDeclaresEveryMethod is the check a consumer writing the RequireAll
// themselves would not have. authorization/grpc is fail-closed, so a method
// Require left out is denied, and a denial for want of a declaration looks
// exactly like a policy somebody meant.
func TestRequireDeclaresEveryMethod(T *testing.T) {
	T.Parallel()

	reqs, err := webhooksgrpc.Require(authzgrpc.NewRequirements()).Build()
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

	test.Nil(T, webhooksgrpc.Require(nil))
}
