package grpc_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/authorization"
	authzgrpc "github.com/primandproper/primitives-go/authorization/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// fullMethodNames is every RPC on the service, by the full method name the
// permission map and the enforcer both key on.
//
// It is read off the generated service descriptor rather than listed, so an RPC
// added to the .proto is one this file has an opinion about immediately.
func fullMethodNames() []string {
	desc := waitlistspb.WaitlistsService_ServiceDesc

	out := make([]string, 0, len(desc.Methods))
	for _, m := range desc.Methods {
		out = append(out, "/"+desc.ServiceName+"/"+m.MethodName)
	}

	return out
}

// administrativeMethods is every RPC that is not in PublicMethods, by the bare
// name the generated handler carries.
func administrativeMethods(tb testing.TB) []string {
	tb.Helper()

	public := waitlistsgrpc.PublicMethods()

	out := make([]string, 0, len(waitlistspb.WaitlistsService_ServiceDesc.Methods))

	for _, full := range fullMethodNames() {
		if slices.Contains(public, full) {
			continue
		}

		out = append(out, full[strings.LastIndex(full, "/")+1:])
	}

	must.SliceNotEmpty(tb, out)

	return out
}

// invokeByName calls one RPC through the generated service descriptor, with a
// request message left at its zero value.
//
// It is how the two tests about who may reach a method cover every method rather
// than the ones somebody remembered, and the zero request is deliberate: what
// those tests are about is the refusal that happens before a handler has read
// anything out of the request.
func invokeByName(tb testing.TB, h *harness, method string, ctx context.Context) error {
	tb.Helper()

	for _, m := range waitlistspb.WaitlistsService_ServiceDesc.Methods {
		if m.MethodName != method {
			continue
		}

		_, err := m.Handler(h.server, ctx, func(any) error { return nil }, nil)

		return err
	}

	tb.Fatalf("no method named %q on the service descriptor", method)

	return nil
}

// TestEveryMethodIsInExactlyOneHalf is the property this service's authorization
// story rests on, and the reason PublicMethods is a function rather than a
// paragraph.
//
// A method in neither list is denied by authorization/grpc's fail-closed rule
// and nothing reports it at wiring time; a method in both is a permission a
// consumer's policy hands out for a call that never checks it. It reads the
// service descriptor rather than a list, so an RPC added later and decided about
// in neither fails here.
func TestEveryMethodIsInExactlyOneHalf(T *testing.T) {
	T.Parallel()

	permissions := waitlistsgrpc.Permissions()
	public := waitlistsgrpc.PublicMethods()

	methods := fullMethodNames()
	must.SliceNotEmpty(T, methods)

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, permissioned := permissions[method]
			isPublic := slices.Contains(public, method)

			test.True(t, permissioned != isPublic, test.Sprintf(
				"%s is %s; it has to be exactly one of permissioned and public",
				method, describeHalves(permissioned, isPublic)))
		})
	}
}

func describeHalves(permissioned, isPublic bool) string {
	switch {
	case permissioned && isPublic:
		return "both in the permission map and declared public"
	default:
		return "neither in the permission map nor declared public"
	}
}

// TestNoRowOutlivesItsMethod is the other direction: an entry naming a method
// the service does not declare is a map describing a schema that has moved.
func TestNoRowOutlivesItsMethod(T *testing.T) {
	T.Parallel()

	methods := fullMethodNames()

	for method := range waitlistsgrpc.Permissions() {
		test.SliceContains(T, methods, method, test.Sprintf(
			"the permission map names %q, which this service does not declare", method))
	}

	for _, method := range waitlistsgrpc.PublicMethods() {
		test.SliceContains(T, methods, method, test.Sprintf(
			"PublicMethods names %q, which this service does not declare", method))
	}
}

// TestThePublicThreeAreTheSignupPage names them, because a roster that only
// counted would let one be swapped for another.
//
// The one that would be added in good faith is GetSignupByContact — it is a
// read, it looks harmless beside Join, and answering it to anybody who can reach
// the port makes the surface an oracle over which addresses are on which list.
func TestThePublicThreeAreTheSignupPage(T *testing.T) {
	T.Parallel()

	test.Eq(T, []string{
		waitlistspb.WaitlistsService_ListOpenLists_FullMethodName,
		waitlistspb.WaitlistsService_Join_FullMethodName,
		waitlistspb.WaitlistsService_Withdraw_FullMethodName,
	}, waitlistsgrpc.PublicMethods())

	_, permissioned := waitlistsgrpc.Permissions()[waitlistspb.WaitlistsService_GetSignupByContact_FullMethodName]
	test.True(T, permissioned, test.Sprint(
		"GetSignupByContact requires no grant; it is the read that would make this surface an oracle"))
}

// TestPermissionsReturnsAFreshMap covers the property a consumer relies on when
// they compose this fragment into their policy and then override an entry.
func TestPermissionsReturnsAFreshMap(T *testing.T) {
	T.Parallel()

	first := waitlistsgrpc.Permissions()
	first[waitlistspb.WaitlistsService_GetList_FullMethodName] = nil

	second := waitlistsgrpc.Permissions()
	test.SliceNotEmpty(T, second[waitlistspb.WaitlistsService_GetList_FullMethodName])
}

// TestPermissionStringsAreNamespaced keeps the grants from colliding with
// another domain's fragment on a word like "read".
func TestPermissionStringsAreNamespaced(T *testing.T) {
	T.Parallel()

	seen := map[authorization.Permission]struct{}{}

	for method, perms := range waitlistsgrpc.Permissions() {
		must.SliceNotEmpty(T, perms, must.Sprintf("%s requires no permission and is not declared public", method))

		for _, perm := range perms {
			test.StrHasPrefix(T, "waitlists.", string(perm))
			seen[perm] = struct{}{}
		}
	}

	// Ten grants over fourteen RPCs: the two reads each cover a get and its
	// list, and everything else is its own.
	test.MapLen(T, 10, seen)
}

// TestRequireDeclaresEveryMethod is the acceptance test for the one call a
// consumer makes.
//
// authorization/grpc is fail-closed, so a method declared nowhere is denied.
// What a consumer needs from Require is that it stays correct when this
// service's method set changes, and what that means concretely is that the
// requirements it builds have an entry for every method on the descriptor.
func TestRequireDeclaresEveryMethod(T *testing.T) {
	T.Parallel()

	reqs, err := waitlistsgrpc.Require(authzgrpc.NewRequirements()).Build()
	must.NoError(T, err)
	must.NotNil(T, reqs)

	declared := reqs.Methods()

	for _, method := range fullMethodNames() {
		test.SliceContains(T, declared, method, test.Sprintf(
			"%s is declared nowhere, so the enforcer denies it", method))
	}
}

// TestRequireToleratesANilBuilder keeps a consumer composing several domains'
// fragments in a chain from needing a nil check per link.
func TestRequireToleratesANilBuilder(T *testing.T) {
	T.Parallel()

	test.Nil(T, waitlistsgrpc.Require(nil))
}
