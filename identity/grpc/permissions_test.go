package grpc_test

import (
	"slices"
	"testing"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	authzgrpc "github.com/primandproper/primitives-go/authorization/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// serviceMethods is every RPC the generated service descriptor declares, as the
// full method names the interceptor matches on.
//
// It is read off the descriptor rather than written down, which is the whole
// point: a list here would be a third place to forget an RPC, and the two tests
// below exist because the first two are easy to forget.
func serviceMethods() []string {
	prefix := "/" + identitypb.IdentityService_ServiceDesc.ServiceName + "/"

	out := make([]string, 0, len(identitypb.IdentityService_ServiceDesc.Methods))
	for _, m := range identitypb.IdentityService_ServiceDesc.Methods {
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

	permissions := identitygrpc.Permissions()
	selfService := identitygrpc.SelfServiceMethods()

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

// TestNoDecisionOutlivesItsMethod is the other direction, and the one a rename
// breaks. An entry naming an RPC the service no longer has reads exactly like a
// live one, and the permission it names then guards nothing.
func TestNoDecisionOutlivesItsMethod(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()

	for method := range identitygrpc.Permissions() {
		test.True(T, slices.Contains(methods, method), test.Sprintf(
			"Permissions names %s, which is not an RPC on this service", method))
	}

	for _, method := range identitygrpc.SelfServiceMethods() {
		test.True(T, slices.Contains(methods, method), test.Sprintf(
			"SelfServiceMethods names %s, which is not an RPC on this service", method))
	}
}

// TestNoPermissionIsEmpty covers what authorization/grpc's builder refuses:
// an empty requirement list is ErrNoPermissionsRequired, and an empty
// permission string is ErrEmptyPermission. Both are easy to write and neither
// is visible until Build is called.
func TestNoPermissionIsEmpty(T *testing.T) {
	T.Parallel()

	for method, perms := range identitygrpc.Permissions() {
		test.SliceNotEmpty(T, perms, test.Sprintf("%s requires an empty permission set", method))

		for _, p := range perms {
			test.NotEqOp(T, "", p, test.Sprintf("%s requires an empty permission", method))
		}
	}
}

// TestTheFragmentComposes is the acceptance test for what this map is for: a
// consumer hands it to authorization/grpc and gets a Requirements out. It is
// asserted here rather than left to a consumer to discover, because every
// failure Build reports — a duplicate method, an empty requirement — is one this
// package could have committed.
func TestTheFragmentComposes(T *testing.T) {
	T.Parallel()

	reqs, err := identitygrpc.Require(authzgrpc.NewRequirements()).Build()
	must.NoError(T, err)
	must.NotNil(T, reqs)

	// Every RPC is declared, which is what keeps the enforcer's fail-closed rule
	// from denying a method this package meant to allow.
	test.SliceLen(T, len(serviceMethods()), reqs.Methods())
}

// TestSelfServiceMethodsTakeNoSubject is the property that makes a self-service
// method safe without a permission: it has no way to name somebody else.
//
// It is asserted against the request messages rather than against prose, so an
// RPC that grows a target_user_id and stays on the self-service list fails here.
// The walk descends into nested messages, because UpdateProfileRequest is one
// field wrapping a ProfileUpdateInput and a user_id added to the input would
// otherwise be one level too deep for the check to see.
//
// email_address and username are subject fields on a request and not on the
// update input: the input's are the caller's own new values, which is the
// self-service act itself, and the depth the descriptor is at tells the two
// apart.
func TestSelfServiceMethodsTakeNoSubject(T *testing.T) {
	T.Parallel()

	// The fields a request would use to name a subject other than the caller.
	subjectFields := []string{"user_id", "email_address", "username"}

	// The fields that name the caller's own new values rather than a subject,
	// by the message that carries them.
	ownValues := map[protoreflect.FullName][]string{
		(&identitypb.ProfileUpdateInput{}).ProtoReflect().Descriptor().FullName(): {"email_address", "username"},
	}

	for _, method := range identitygrpc.SelfServiceMethods() {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			walkFields(requestDescriptorFor(t, method), func(md protoreflect.MessageDescriptor, fd protoreflect.FieldDescriptor) {
				name := string(fd.Name())
				if slices.Contains(ownValues[md.FullName()], name) {
					return
				}

				test.False(t, slices.Contains(subjectFields, name), test.Sprintf(
					"%s is self-service and its request carries %s.%s, which names somebody other than the caller",
					method, md.FullName(), name))
			})
		})
	}
}

// TestRequireDeclaresEveryMethod is what the helper exists for. The hand-written
// version of it — RequireAll, then a loop of Public — is a hole that fails
// silently: forget the loop and the builder still Builds, and the enforcer then
// denies the eight self-service methods as undeclared, GetPrincipal included.
//
// So this asserts the property rather than the calls: after Require, the
// requirements name every RPC on the service.
func TestRequireDeclaresEveryMethod(T *testing.T) {
	T.Parallel()

	reqs, err := identitygrpc.Require(authzgrpc.NewRequirements()).Build()
	must.NoError(T, err)

	declared := reqs.Methods()

	for _, method := range serviceMethods() {
		test.SliceContains(T, declared, method, test.Sprintf(
			"%s is not declared after Require, so the enforcer will deny it as undeclared", method))
	}
}

// TestRequireComposes covers the shape a consumer with more than one domain
// actually writes: the builder comes in and goes out, so other services and
// their own public methods join the same table.
func TestRequireComposes(T *testing.T) {
	T.Parallel()

	const otherMethod = "/some.other.Service/Method"

	reqs, err := identitygrpc.Require(authzgrpc.NewRequirements()).
		Public(otherMethod).
		Build()
	must.NoError(T, err)

	test.SliceContains(T, reqs.Methods(), otherMethod)
	test.SliceLen(T, len(serviceMethods())+1, reqs.Methods())
}

// TestRequireToleratesANilBuilder keeps the helper from panicking where every
// other constructor in this module returns an error.
func TestRequireToleratesANilBuilder(T *testing.T) {
	T.Parallel()

	test.Nil(T, identitygrpc.Require(nil))
}
