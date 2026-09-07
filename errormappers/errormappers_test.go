package errormappers_test

import (
	"context"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/errormappers"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	grpcerrors "github.com/primandproper/platform-go/v14/errors/grpc"
	httperrors "github.com/primandproper/platform-go/v14/errors/http"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/internal/sentinelmatrix"
	"github.com/primandproper/platform-go/v14/links"

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

// TestRegister_installsTheClientSafeSentinels covers the other half of what
// links and identity need from gRPC. Their outcomes share codes, so the
// message is the only place the difference between "already used" and
// "expired", or between a taken username and a taken email address, survives,
// and a gRPC message is the code's name unless a sentinel is registered as safe
// to quote.
func TestRegister_installsTheClientSafeSentinels(T *testing.T) {
	T.Parallel()

	interceptor := grpcerrors.UnaryErrorEncodingInterceptor()

	seen := map[string]struct{}{}

	sentinels := slices.Concat(links.ClientSafeSentinels, identity.ClientSafeSentinels)

	for _, sentinel := range sentinels {
		_, err := interceptor(
			T.Context(),
			nil,
			&grpc.UnaryServerInfo{},
			func(context.Context, any) (any, error) {
				return nil, platformerrors.Wrap(sentinel, "redeeming action link")
			},
		)
		must.Error(T, err)

		st, ok := status.FromError(err)
		must.True(T, ok)

		test.EqOp(T, sentinel.Error(), st.Message(), test.Sprintf(
			"%v reached the client as %q, which is the code's name rather than its own", sentinel, st.Message()))

		seen[st.Message()] = struct{}{}
	}

	test.MapLen(T, len(sentinels), seen)
}
