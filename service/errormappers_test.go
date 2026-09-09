package service

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/internal/sentinelmatrix"
	"github.com/primandproper/platform-go/v14/links"

	platformerrors "github.com/primandproper/primitives-go/errors"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRegister_installsTheDomainErrorMappers is the other end of the inversion
// that moved these mappings out of errors/http and errors/grpc. Those two are
// primitives and answer only for primitives now, so a privacy request nobody
// registered a mapper for reaches a subject as a 500 — the exact failure the
// mappings were written to end. This is what registers them, through the one
// call in errormappers.
//
// The Config names no DataPrivacy and no Operations, which is the point: those
// two used to be registered inside their config blocks and are now registered
// for every service, on the argument the other two always made — an unused
// mapper is one comparison against a sentinel the service cannot produce, and
// the expensive direction to be wrong in is a 500 for an error somebody took the
// trouble to give a status. No other test in this package builds a Config with
// either field set, so a mapper reaching a sentinel here reached it from this
// call.
//
// The expectation is what the owning package's mapper answers, computed in
// internal/sentinelmatrix, and errormappers' own test asserts the same values
// against the same source. That is what makes the one call and this one unable
// to drift apart: neither test carries a table of its own to disagree with.
func TestRegister_installsTheDomainErrorMappers(T *testing.T) {
	T.Parallel()

	newInjector(T, &Config{Name: "example"})

	resolutions := sentinelmatrix.MappedResolutions()
	must.SliceNotEmpty(T, resolutions, must.Sprint("no mapped sentinels, so this test asserted nothing"))

	for _, want := range resolutions {
		T.Run(want.Package+"."+want.Name, func(t *testing.T) {
			t.Parallel()

			// Wrapped, because that is how one arrives from a handler.
			err := platformerrors.Wrap(want.Err, "serving a request")

			code, msg := httperrors.ToAPIError(err)
			test.EqOp(t, want.HTTPCode, code, test.Sprintf(
				"%s.%s resolved to %v after Register and %v through %s's own mapper",
				want.Package, want.Name, code, want.HTTPCode, want.Package))
			test.EqOp(t, want.HTTPMsg, msg)
			test.NotEqOp(t, httperrors.ErrNothingSpecific, code, test.Sprintf(
				"%s.%s resolved to the neutral code, so no HTTP mapper was registered for %s",
				want.Package, want.Name, want.Package))

			grpcCode := grpcerrors.MapToGRPC(err, codes.Unknown)
			test.EqOp(t, want.GRPCCode, grpcCode, test.Sprintf(
				"%s.%s resolved to %v after Register and %v through %s's own mapper",
				want.Package, want.Name, grpcCode, want.GRPCCode, want.Package))
			test.NotEqOp(t, codes.Unknown, grpcCode, test.Sprintf(
				"%s.%s resolved to codes.Unknown, so no gRPC mapper was registered for %s",
				want.Package, want.Name, want.Package))
		})
	}
}

// TestRegister_installsTheClientSafeSentinels covers the other half of what
// links needs from gRPC. Its four redemption outcomes share one code, so the
// message is the only place the difference between "already used" and "expired"
// survives, and a gRPC message is the code's name unless a sentinel is
// registered as safe to quote.
func TestRegister_installsTheClientSafeSentinels(T *testing.T) {
	T.Parallel()

	newInjector(T, &Config{Name: "example"})

	interceptor := grpcerrors.UnaryErrorEncodingInterceptor()

	seen := map[string]struct{}{}

	for _, sentinel := range links.ClientSafeSentinels {
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

	test.MapLen(T, len(links.ClientSafeSentinels), seen)
}
