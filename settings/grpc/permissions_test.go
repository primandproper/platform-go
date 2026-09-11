package grpc_test

import (
	"slices"
	"testing"

	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"

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
	prefix := "/" + settingspb.SettingsService_ServiceDesc.ServiceName + "/"

	out := make([]string, 0, len(settingspb.SettingsService_ServiceDesc.Methods))
	for _, m := range settingspb.SettingsService_ServiceDesc.Methods {
		out = append(out, prefix+m.MethodName)
	}

	return out
}

// TestEveryMethodIsPermissioned is the reason this file exists, and it is the
// decision this service made rather than a property it happens to have.
//
// There is no second set of methods here that require nothing. The catalog is a
// deployment's own configuration and the answers are somebody's own choices
// about themselves, and neither is a thing an unauthorized caller reads because
// they happened to know a name.
func TestEveryMethodIsPermissioned(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("no methods on the service descriptor, so this asserted nothing"))

	permissions := settingsgrpc.Permissions()

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

	for method := range settingsgrpc.Permissions() {
		test.True(T, slices.Contains(methods, method), test.Sprintf(
			"Permissions names %s, which the service descriptor does not declare", method))
	}
}

// TestNoPermissionIsEmpty catches the decision that looks made and is not: an
// entry mapping a method to an empty slice declares it and requires nothing.
func TestNoPermissionIsEmpty(T *testing.T) {
	T.Parallel()

	for method, permissions := range settingsgrpc.Permissions() {
		test.SliceNotEmpty(T, permissions, test.Sprintf(
			"%s is declared with no permissions, which grants it to everybody", method))
	}
}

// TestTheTwoAudiencesDoNotShareAGrant is the split this package's two halves
// bought.
//
// Reading the catalog is what a settings screen does before it renders
// anything; rewriting a definition decides how every value already stored is
// read. A consumer whose policy could not tell those apart would be granting
// the second to everybody who needs the first.
func TestTheTwoAudiencesDoNotShareAGrant(T *testing.T) {
	T.Parallel()

	permissions := settingsgrpc.Permissions()

	read := permissions[settingspb.SettingsService_ListDefinitions_FullMethodName]
	update := permissions[settingspb.SettingsService_UpdateDefinition_FullMethodName]

	must.SliceNotEmpty(T, read)
	must.SliceNotEmpty(T, update)

	test.False(T, slices.Contains(read, settingsgrpc.PermissionUpdateDefinitions))
	test.False(T, slices.Contains(update, settingsgrpc.PermissionReadDefinitions))

	// And a subject's own answers are not reachable with the grant that reads
	// everybody's. The second names every subject in the scope, which is why it
	// is its own grant rather than the one a preferences page holds.
	own := permissions[settingspb.SettingsService_ListValuesForSubject_FullMethodName]
	all := permissions[settingspb.SettingsService_ListValuesForDefinition_FullMethodName]

	test.False(T, slices.Contains(own, settingsgrpc.PermissionReadAllValues))
	test.False(T, slices.Contains(all, settingsgrpc.PermissionReadValues))
}

// TestSetAndClearShareOneGrant is the other half of that: they are one
// capability, and a deployment that let somebody choose a value and not return
// to the default would be one where the only way out of a choice is another
// choice.
func TestSetAndClearShareOneGrant(T *testing.T) {
	T.Parallel()

	permissions := settingsgrpc.Permissions()

	test.Eq(T,
		permissions[settingspb.SettingsService_SetValue_FullMethodName],
		permissions[settingspb.SettingsService_ClearValue_FullMethodName])
}

// TestPermissionsReturnsAFreshMap keeps a consumer's override from editing
// every other consumer's copy in the same process.
func TestPermissionsReturnsAFreshMap(T *testing.T) {
	T.Parallel()

	first := settingsgrpc.Permissions()
	for method := range first {
		delete(first, method)
	}

	test.MapNotEmpty(T, settingsgrpc.Permissions(),
		test.Sprint("Permissions handed back a map a caller could empty for everybody"))
}

// TestRequireDeclaresEveryMethod is the check a consumer writing the RequireAll
// themselves would not have. authorization/grpc is fail-closed, so a method
// Require left out is denied, and a denial for want of a declaration looks
// exactly like a policy somebody meant.
func TestRequireDeclaresEveryMethod(T *testing.T) {
	T.Parallel()

	reqs, err := settingsgrpc.Require(authzgrpc.NewRequirements()).Build()
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

	test.Nil(T, settingsgrpc.Require(nil))
}
