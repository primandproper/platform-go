package signin

import (
	"context"
	"testing"

	domain "github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/conformance"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// withReasons runs assert inside a session whose subject says stripped.
func withReasons(t *testing.T, stripped bool, assert func(*testing.T, *conformance.Session)) {
	t.Helper()

	conformance.Run(t, conformance.Seams{
		NewSubject: func(context.Context, ...conformance.SubjectOption) (*conformance.Subject, error) {
			return &conformance.Subject{}, nil
		},
		ErrorReasonsStripped: stripped,
	}, conformance.Suite{Name: "reasons", Run: assert})
}

func TestReasons(T *testing.T) {
	T.Parallel()

	// What a deployment whose edge rebuilt the status without its details
	// answers: the right code, and nothing to branch on.
	stripped := status.Error(codes.Unauthenticated, "invalid credentials")

	carried, err := status.New(codes.Unauthenticated, "invalid credentials").WithDetails(&errdetails.ErrorInfo{
		Reason: reasonInvalidCredentials,
		Domain: domain.ClientReasonDomain,
	})
	must.NoError(T, err)

	T.Run("a subject that says nothing has the reason asserted", func(t *testing.T) {
		t.Parallel()

		withReasons(t, false, func(t *testing.T, s *conformance.Session) {
			t.Helper()

			must.True(t, reasons(t, s))
			must.EqOp(t, reasonInvalidCredentials, reason(carried.Err()))
			refused(t, s, carried.Err(), codes.Unauthenticated, reasonInvalidCredentials)
		})
	})

	T.Run("a subject whose edge strips reasons has the code asserted and the reason skipped", func(t *testing.T) {
		t.Parallel()

		withReasons(t, true, func(t *testing.T, s *conformance.Session) {
			t.Helper()

			must.False(t, reasons(t, s))
			refused(t, s, stripped, codes.Unauthenticated, reasonInvalidCredentials)
			indistinguishable(t, s, stripped, stripped, "two stripped refusals")
		})
	})

	// Why the default is to assert: the edge the contract tells a deployment to
	// put in front of untrusted clients takes the encoded chain and leaves the
	// reason, so a deployment that did exactly that still owes it.
	T.Run("the reason survives an edge that strips the encoded chain", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, reasonInvalidCredentials, reason(grpcerrors.StripEncodedErrorDetail(carried.Err())))
	})
}
