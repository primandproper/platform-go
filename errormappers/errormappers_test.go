package errormappers_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/errormappers"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/internal/sentinelmatrix"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestMain registers once for the whole binary, which is the only honest way to
// test a process-global registry: every test below asks ToAPIError and MapToGRPC
// what a wrapped sentinel resolves to, and those read a registry the process has
// exactly one of. Registering inside each test would work too, and would be
// asserting that appending the same mappers four times is harmless rather than
// that appending them once is enough.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// TestRegister_resolvesEveryMappedSentinel is the acceptance test for the one
// call: after it, every sentinel internal/sentinelmatrix records as mapped
// reaches a client as the status its own package decided on, on both transports.
//
// The expectation comes from the owning package's mappers rather than from a
// table here, so this asserts the registration and not the mapping — a mapper's
// own cases are tested in its own package, and the roster is checked against
// those packages' source in internal/sentinelmatrix. service.Register asserts
// the same thing against the same expectation, which is what keeps the one call
// and the config-driven one from answering a sentinel differently.
func TestRegister_resolvesEveryMappedSentinel(T *testing.T) {
	T.Parallel()

	resolutions := sentinelmatrix.MappedResolutions()
	must.SliceNotEmpty(T, resolutions, must.Sprint("no mapped sentinels, so this test asserted nothing"))

	for _, want := range resolutions {
		T.Run(want.Package+"."+want.Name, func(t *testing.T) {
			t.Parallel()

			// Wrapped, because that is how one arrives from a handler.
			err := platformerrors.Wrap(want.Err, "serving a request")

			code, msg := httperrors.ToAPIError(err)
			test.EqOp(t, want.HTTPCode, code, test.Sprintf(
				"%s.%s resolved to %v through the registry and %v through %s's own mapper",
				want.Package, want.Name, code, want.HTTPCode, want.Package))
			test.EqOp(t, want.HTTPMsg, msg)
			test.NotEqOp(t, httperrors.ErrNothingSpecific, code, test.Sprintf(
				"%s.%s resolved to the neutral code, so no HTTP mapper was registered for %s",
				want.Package, want.Name, want.Package))

			grpcCode := grpcerrors.MapToGRPC(err, codes.Unknown)
			test.EqOp(t, want.GRPCCode, grpcCode, test.Sprintf(
				"%s.%s resolved to %v through the registry and %v through %s's own mapper",
				want.Package, want.Name, grpcCode, want.GRPCCode, want.Package))
			test.NotEqOp(t, codes.Unknown, grpcCode, test.Sprintf(
				"%s.%s resolved to codes.Unknown, so no gRPC mapper was registered for %s",
				want.Package, want.Name, want.Package))
		})
	}
}

// TestRegister_installsTheClientSafeSentinels covers the other half of what the
// packages in internal/sentinelmatrix's client-safe roster need from gRPC.
// Their outcomes share codes, so the message is the only place the difference
// between "already used" and "expired", or between a taken username and a taken
// email address, survives, and a gRPC message is the code's name unless a
// sentinel is registered as safe to quote.
//
// The lists come from the roster rather than from names spelled here, which is
// what makes this the assertion the prose points at. It used to name links and
// identity, so the seven packages that declared a list afterwards were
// registered by Register and asserted by nothing — and a list registered nowhere
// has no symptom in its own package's tests.
func TestRegister_installsTheClientSafeSentinels(T *testing.T) {
	T.Parallel()

	interceptor := grpcerrors.UnaryErrorEncodingInterceptor()

	seen := map[string]struct{}{}

	var sentinels []error
	for _, pkg := range sentinelmatrix.ClientSafePackages {
		sentinels = append(sentinels, sentinelmatrix.ClientSafeSentinels(pkg)...)
	}

	must.SliceNotEmpty(T, sentinels, must.Sprint("no client-safe sentinels, so this test asserted nothing"))

	for _, sentinel := range sentinels {
		_, err := interceptor(
			T.Context(),
			nil,
			&grpc.UnaryServerInfo{},
			func(context.Context, any) (any, error) {
				return nil, platformerrors.Wrap(sentinel, "serving a request")
			},
		)
		must.Error(T, err)

		st, ok := status.FromError(err)
		must.True(T, ok)

		test.EqOp(T, sentinel.Error(), st.Message(), test.Sprintf(
			"%v reached the client as %q, which is the code's name rather than its own", sentinel, st.Message()))

		seen[st.Message()] = struct{}{}
	}

	test.MapLen(T, len(sentinels), seen, test.Sprint(
		"two client-safe sentinels reached a client with the same words, so neither says which happened"))
}

// TestRegister_theOperationSpellingReachesTheRegistry is the acceptance test for
// the one way a handler holding an observability.Operation turns an error into a
// status: errors/grpc's PrepareAndLogGRPCStatus, handed the operation's own two
// pillars.
//
// Operation used to carry a GRPCStatus method, and that method could not consult
// the registry — observability sits below errors/grpc, so the import that would
// let it is a cycle — which meant it answered with whatever code its caller
// guessed. It is gone, and this asserts what replaced it against a domain
// sentinel rather than a platform one on purpose: a platform sentinel resolves
// through PlatformMapper, which is compiled in, so the same assertion would pass
// with nothing registered at all and would be testing the cycle rather than the
// registry.
func TestRegister_theOperationSpellingReachesTheRegistry(T *testing.T) {
	T.Parallel()

	o := observability.NewObserverForTest("errormappers_test")
	_, op := o.Begin(T.Context())

	defer op.End()

	// Wrapped, because that is how one arrives from a handler.
	err := grpcerrors.PrepareAndLogGRPCStatus(
		platformerrors.Wrap(identity.ErrUsernameTaken, "registering the user"),
		op.Logger(),
		op.Span(),
		codes.Internal,
		"registering the user",
	)
	must.Error(T, err)

	test.EqOp(T, codes.AlreadyExists, status.Code(err), test.Sprint(
		"identity.ErrUsernameTaken reached the client as the code the call site passed as a default, "+
			"so this spelling never reached identity.GRPCMapper"))
}
