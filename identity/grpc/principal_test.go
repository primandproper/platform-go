package grpc_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is where [identitygrpc.Principal]'s method set being final stops
// being a sentence in its documentation. Nine sibling packages alias the type
// verbatim, so a method added to it is a method every consumer of every gRPC
// surface in this module has to grow at once, on a session type this module
// never sees — a break with no deprecation available, because an interface
// carries no default.
//
// So the roster below is the ruling rather than a description of it, in the
// mechanism the transport surfaces already use for which store methods cross
// onto the wire: the list is the argument, and a fourth method fails here rather
// than in somebody else's repository.

// principalMethods is the whole of the interface, and the whole of what a
// consumer's session type owes.
var principalMethods = []string{"ActiveAccountID", "Scope", "UserID"}

// TestPrincipalMethodSetIsFinal pins the three, by name and by count.
//
// A fourth arriving here should be read as the fix having been applied in the
// wrong place: what the surface that wanted it reaches for is an optional
// interface, asserted at the call site, which TestPrincipalExtendsByOptionalInterface
// below is the worked shape of.
func TestPrincipalMethodSetIsFinal(T *testing.T) {
	T.Parallel()

	principal := reflect.TypeFor[identitygrpc.Principal]()

	got := make([]string, 0, principal.NumMethod())
	for method := range principal.Methods() {
		got = append(got, method.Name)
	}

	slices.Sort(got)

	test.Eq(T, principalMethods, got,
		test.Sprint("Principal's method set is final; extensions are optional interfaces"))
}

// sessionIdentified is the documented extension shape, declared where it is
// needed rather than on [identitygrpc.Principal]: a surface that wants more of
// its caller than the three asks for it here and type-asserts.
type sessionIdentified interface {
	SessionID() string
}

// sessionPrincipal is the consumer whose session type happens to answer it.
type sessionPrincipal struct {
	sessionID string
	testPrincipal
}

func (p *sessionPrincipal) SessionID() string { return p.sessionID }

// TestPrincipalExtendsByOptionalInterface is the assertion shape Principal's
// documentation shows, executed.
//
// What it pins is the property the ruling turns on: a consumer whose type does
// not answer the optional question is a [identitygrpc.Principal] all the same,
// where a fourth method on the interface would have left them uncompilable.
func TestPrincipalExtendsByOptionalInterface(T *testing.T) {
	T.Parallel()

	T.Run("a caller whose type answers it is asked", func(t *testing.T) {
		t.Parallel()

		var caller identitygrpc.Principal = &sessionPrincipal{
			userID: "caller", scope: tenancy.Global(),
			sessionID: "session-1",
		}

		s, ok := caller.(sessionIdentified)
		must.True(t, ok, must.Sprint("a principal with the method should assert to the optional interface"))
		test.EqOp(t, "session-1", s.SessionID())
	})

	T.Run("a caller whose type does not is still a principal", func(t *testing.T) {
		t.Parallel()

		var caller identitygrpc.Principal = &testPrincipal{userID: "caller", scope: tenancy.Global()}

		_, ok := caller.(sessionIdentified)
		test.False(t, ok, test.Sprint("the base interface should not have grown the optional method"))
		test.EqOp(t, "caller", caller.UserID())
	})
}

// TestPrincipalExtractorReportsAbsence pins the other half of the seam: the
// false return is the answer for an unauthenticated call, and there is no error
// type for this package to have defined instead.
func TestPrincipalExtractorReportsAbsence(T *testing.T) {
	T.Parallel()

	var extract identitygrpc.PrincipalExtractor = func(context.Context) (identitygrpc.Principal, bool) {
		return nil, false
	}

	got, ok := extract(T.Context())
	test.False(T, ok)
	test.Nil(T, got)
}
