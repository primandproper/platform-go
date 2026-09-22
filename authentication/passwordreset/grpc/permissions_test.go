package grpc_test

import (
	"context"
	"slices"
	"testing"

	passwordresetgrpc "github.com/primandproper/platform-go/v14/authentication/passwordreset/grpc"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
)

// serviceMethods is every RPC the generated service descriptor declares, in the
// full-method form an interceptor sees.
//
// Reading it off the descriptor rather than listing it here is what makes this
// file a check rather than a second copy: an RPC added to the schema appears here
// without anybody remembering to add it.
func serviceMethods() []string {
	prefix := "/" + passwordresetpb.PasswordResetService_ServiceDesc.ServiceName + "/"

	out := make([]string, 0, len(passwordresetpb.PasswordResetService_ServiceDesc.Methods))
	for _, m := range passwordresetpb.PasswordResetService_ServiceDesc.Methods {
		out = append(out, prefix+m.MethodName)
	}

	return out
}

// TestMethodsAreDecidedAbout is the property the roster exists for: an RPC added
// to the service later and left out of it is a method the enforcer denies, in
// somebody's production, for a reason nothing connects to a missing entry here.
//
// What it would be denying is the only way back in for somebody who cannot sign
// in, which is why this check matters more on this service than on one whose
// callers can still reach a door.
func TestMethodsAreDecidedAbout(T *testing.T) {
	T.Parallel()

	anonymous := passwordresetgrpc.AnonymousMethods()

	for _, method := range serviceMethods() {
		test.True(T, slices.Contains(anonymous, method), test.Sprintf(
			"%s is on the service and in no list, so nothing says who may call it", method))
	}
}

// TestListsNameOnlyRealMethods is the other direction: a renamed RPC leaves a
// string in a list that no longer matches anything, and a builder cannot tell
// that from a method deliberately absent.
func TestListsNameOnlyRealMethods(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()

	for _, method := range passwordresetgrpc.AnonymousMethods() {
		test.SliceContains(T, methods, method, test.Sprintf(
			"%s is named in a list but is not an RPC on this service", method))
	}
}

func TestRequire(T *testing.T) {
	T.Parallel()

	T.Run("declares every method as public", func(t *testing.T) {
		t.Parallel()

		reqs, err := passwordresetgrpc.Require(authzgrpc.NewRequirements()).Build()
		must.NoError(t, err)

		test.SliceLen(t, len(serviceMethods()), reqs.Methods())

		for _, method := range serviceMethods() {
			test.SliceContains(t, reqs.Methods(), method)
		}
	})

	T.Run("composes onto a builder that already has entries", func(t *testing.T) {
		t.Parallel()

		const otherMethod = "/consumer.v1.Meals/GetMeal"

		builder := authzgrpc.NewRequirements()
		builder.Public(otherMethod)

		reqs, err := passwordresetgrpc.Require(builder).Build()
		must.NoError(t, err)

		test.SliceContains(t, reqs.Methods(), otherMethod)
		test.SliceLen(t, len(serviceMethods())+1, reqs.Methods())
	})

	T.Run("a nil builder", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, passwordresetgrpc.Require(nil))
	})
}

// TestNothingIsPermissioned pins the conclusion the package documentation argues
// for, so that a permission added later is a deliberate change to this file
// rather than a quiet one.
//
// It asserts it the way it will actually be experienced: an enforcer built over
// this fragment lets every method through for a caller who holds no grants at
// all — which is every caller this service has, since none of them has signed in.
func TestNothingIsPermissioned(T *testing.T) {
	T.Parallel()

	reqs, err := passwordresetgrpc.Require(authzgrpc.NewRequirements()).Build()
	must.NoError(T, err)

	enforcer, err := authzgrpc.NewEnforcer(reqs,
		func(context.Context) (authorization.Grants, bool) { return authorization.NewGrants(), true })
	must.NoError(T, err)

	interceptor := enforcer.UnaryServerInterceptor()

	for _, method := range serviceMethods() {
		reached := false

		_, err = interceptor(T.Context(), nil, &grpc.UnaryServerInfo{FullMethod: method},
			func(ctx context.Context, _ any) (any, error) {
				reached = true

				return nil, nil
			})

		test.NoError(T, err, test.Sprintf("%s was refused", method))
		test.True(T, reached, test.Sprintf("%s never reached its handler", method))
	}
}
