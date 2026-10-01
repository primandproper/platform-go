package grpc_test

import (
	"context"
	"maps"
	"slices"
	"testing"

	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
)

// methodsOf is every RPC a generated service descriptor declares, in the
// full-method form an interceptor sees.
//
// Reading it off the descriptor rather than listing it here is what makes this
// file a check rather than a second copy: an RPC added to the schema appears
// here without anybody remembering to add it.
func methodsOf(desc *grpc.ServiceDesc) []string {
	prefix := "/" + desc.ServiceName + "/"

	out := make([]string, 0, len(desc.Methods))
	for _, m := range desc.Methods {
		out = append(out, prefix+m.MethodName)
	}

	return out
}

// signInMethods are SignInService's RPCs, none of them permissioned.
func signInMethods() []string {
	return methodsOf(&signinpb.SignInService_ServiceDesc)
}

// administrationMethods are SignInAdministrationService's RPCs, every one of
// them permissioned.
func administrationMethods() []string {
	return methodsOf(&signinpb.SignInAdministrationService_ServiceDesc)
}

// serviceMethods is every RPC Server serves, across both services.
func serviceMethods() []string {
	return slices.Concat(signInMethods(), administrationMethods())
}

// permissioned is the methods Permissions names.
func permissioned() []string {
	return slices.Collect(maps.Keys(signingrpc.Permissions()))
}

// TestMethodsAreDecidedAbout is the property the two lists and the map exist for: an RPC
// added to the service later and named in none of them is a method the enforcer
// denies, in somebody's production, for a reason nothing connects to a missing
// entry here.
func TestMethodsAreDecidedAbout(T *testing.T) {
	T.Parallel()

	lists := map[string][]string{
		"AnonymousMethods":   signingrpc.AnonymousMethods(),
		"SelfServiceMethods": signingrpc.SelfServiceMethods(),
		"Permissions":        permissioned(),
	}

	for _, method := range serviceMethods() {
		naming := make([]string, 0, len(lists))

		for name, list := range lists {
			if slices.Contains(list, method) {
				naming = append(naming, name)
			}
		}

		test.SliceLen(T, 1, naming, test.Sprintf(
			"%s is named by %v, and every RPC belongs to exactly one list: none means nothing says who may call it, "+
				"and two means authorization/grpc refuses a method declared twice", method, naming))
	}
}

// TestListsNameOnlyRealMethods is the other direction: a renamed RPC leaves a
// string in a list that no longer matches anything, and a builder cannot tell
// that from a method deliberately absent.
func TestListsNameOnlyRealMethods(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()

	for _, method := range slices.Concat(
		signingrpc.AnonymousMethods(),
		signingrpc.SelfServiceMethods(),
		permissioned(),
	) {
		test.SliceContains(T, methods, method, test.Sprintf(
			"%s is named in a list but is not an RPC on this service", method))
	}
}

func TestRequire(T *testing.T) {
	T.Parallel()

	T.Run("declares every method", func(t *testing.T) {
		t.Parallel()

		reqs, err := signingrpc.Require(authzgrpc.NewRequirements()).Build()
		must.NoError(t, err)

		// Every one, and nothing else. A method this service serves and does not
		// declare is denied by the enforcer's fail-closed rule, which is the
		// failure this function exists to make impossible.
		test.SliceLen(t, len(serviceMethods()), reqs.Methods())

		for _, method := range serviceMethods() {
			test.SliceContains(t, reqs.Methods(), method)
		}
	})

	T.Run("composes onto a builder that already has entries", func(t *testing.T) {
		t.Parallel()

		const otherMethod = "/consumer.v1.Newsletters/GetNewsletter"

		builder := authzgrpc.NewRequirements()
		builder.Public(otherMethod)

		reqs, err := signingrpc.Require(builder).Build()
		must.NoError(t, err)

		test.SliceContains(t, reqs.Methods(), otherMethod)
		test.SliceLen(t, len(serviceMethods())+1, reqs.Methods())
	})

	T.Run("a nil builder", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, signingrpc.Require(nil))
	})
}

// TestNothingIsPermissioned pins the conclusion the package documentation
// argues for about SignInService, so that a permission added later is a
// deliberate change to this file rather than a quiet one.
//
// It asserts it the way it will actually be experienced: an enforcer built over
// this fragment lets every method through for a caller who holds no grants at
// all. The anonymous ones are how a caller becomes somebody — signing up
// included — or stops being them, so no grant could gate them; the rest take
// their subject from the principal and have no field that could name anybody
// else.
func TestNothingIsPermissioned(T *testing.T) {
	T.Parallel()

	interceptor := enforcing(T)

	for _, method := range signInMethods() {
		reached, err := reaches(T, interceptor, method)

		test.NoError(T, err, test.Sprintf("%s was refused", method))
		test.True(T, reached, test.Sprintf("%s never reached its handler", method))
	}
}

// TestAdministrationIsPermissioned is the other half: every
// SignInAdministrationService method is refused to a caller holding no grants,
// admitted to one holding the permission Permissions names for it, and refused
// to one holding only the other — so a grant to read somebody's logins is not
// a grant to end them.
func TestAdministrationIsPermissioned(T *testing.T) {
	T.Parallel()

	for _, method := range administrationMethods() {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			required := signingrpc.Permissions()[method]
			must.SliceNotEmpty(t, required)

			_, err := reaches(t, enforcing(t), method)
			test.Error(t, err, test.Sprintf("%s was admitted to a caller holding no grants", method))

			reached, err := reaches(t, enforcing(t, required...), method)
			test.NoError(t, err)
			test.True(t, reached, test.Sprintf("%s refused a caller holding %v", method, required))

			other := signingrpc.PermissionReadAnySignIns
			if slices.Contains(required, other) {
				other = signingrpc.PermissionEndAnySignIns
			}

			_, err = reaches(t, enforcing(t, other), method)
			test.Error(t, err, test.Sprintf("%s was admitted to a caller holding only %s", method, other))
		})
	}
}

// enforcing is an enforcer over Require's fragment, for a caller holding
// exactly held.
func enforcing(t *testing.T, held ...authorization.Permission) grpc.UnaryServerInterceptor {
	t.Helper()

	reqs, err := signingrpc.Require(authzgrpc.NewRequirements()).Build()
	must.NoError(t, err)

	grants := authorization.NewGrants(authorization.NewPermissionSet(held...))

	enforcer, err := authzgrpc.NewEnforcer(reqs,
		func(context.Context) (authorization.Grants, bool) { return grants, true })
	must.NoError(t, err)

	return enforcer.UnaryServerInterceptor()
}

// reaches makes method through interceptor and reports whether it got as far
// as a handler.
func reaches(t *testing.T, interceptor grpc.UnaryServerInterceptor, method string) (bool, error) {
	t.Helper()

	reached := false

	_, err := interceptor(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: method},
		func(context.Context, any) (any, error) {
			reached = true

			return nil, nil
		})

	return reached, err
}
