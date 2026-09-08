package dataprivacy

import (
	"net/http"
	"testing"

	platformerrors "github.com/primandproper/primitives-go/errors"
	httperrors "github.com/primandproper/primitives-go/errors/http"

	"github.com/shoenig/test"
	"google.golang.org/grpc/codes"
)

// mappedSentinels is every sentinel in this package both mappers are expected to
// have an answer for.
//
// One list rather than one per transport, for the reason errors' own parity test
// gives: a service exposing both would otherwise answer a missing request with a
// considered 404 on one and codes.Unknown on the other, and which one a client
// got would depend on how it happened to connect.
//
// The rest of this package's sentinels are wiring failures and fulfillment-side
// outcomes — no collectors registered, a collector that panicked, an upload
// manager that cannot sign a URL — and reach a subject only through a service
// that shipped broken, where a 500 is the honest answer.
// internal/sentinelmatrix is where that split is written down and checked
// against this file.
var mappedSentinels = []error{
	ErrRequestNotFound,
	ErrNotAwaitingConfirmation,
	ErrArtifactUnavailable,
	ErrEmptySubjectID,
	ErrUnknownRequestType,
}

func TestMappers_coverTheSameSentinels(T *testing.T) {
	T.Parallel()

	for _, sentinel := range mappedSentinels {
		T.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()

			code, msg, httpOK := HTTPMapper.Map(sentinel)
			test.True(t, httpOK, test.Sprintf("no HTTP mapping for %v", sentinel))
			test.NotEq(t, httperrors.ErrorCode(""), code)
			test.NotEq(t, "", msg)

			grpcCode, grpcOK := GRPCMapper.Map(sentinel)
			test.True(t, grpcOK, test.Sprintf("no gRPC mapping for %v", sentinel))
			test.NotEqOp(t, codes.Unknown, grpcCode)
		})
	}
}

func TestMappers_wrappedSentinelsStillMap(T *testing.T) {
	T.Parallel()

	// The mappers are reached from handlers, which wrap. A mapping that only
	// works on a bare sentinel works nowhere real.
	for _, sentinel := range mappedSentinels {
		T.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()

			wrapped := platformerrors.Wrap(sentinel, "reading privacy request")

			_, _, httpOK := HTTPMapper.Map(wrapped)
			test.True(t, httpOK)

			_, grpcOK := GRPCMapper.Map(wrapped)
			test.True(t, grpcOK)
		})
	}
}

// The gap this mapping was written to close: a subject asking after their own
// export got a 500 saying the service was broken, when the answer was that the
// ID was not one of theirs.
func TestMappers_aMissingRequestIsANotFound(T *testing.T) {
	T.Parallel()

	code, _, ok := HTTPMapper.Map(ErrRequestNotFound)
	test.True(T, ok)
	test.EqOp(T, httperrors.ErrDataNotFound, code)
	test.EqOp(T, http.StatusNotFound, httperrors.HTTPStatusForCode(code))

	grpcCode, grpcOK := GRPCMapper.Map(ErrRequestNotFound)
	test.True(T, grpcOK)
	test.EqOp(T, codes.NotFound, grpcCode)
}

// A conflict rather than a not-found: the request exists and the caller may see
// it, it is simply not in the state the call needs, and the remedy is a state
// change somebody else makes rather than a corrected request.
func TestMappers_aWrongStateIsAConflict(T *testing.T) {
	T.Parallel()

	for _, sentinel := range []error{ErrNotAwaitingConfirmation, ErrArtifactUnavailable} {
		code, _, ok := HTTPMapper.Map(sentinel)
		test.True(T, ok)
		test.EqOp(T, httperrors.ErrResourceConflict, code)
		test.EqOp(T, http.StatusConflict, httperrors.HTTPStatusForCode(code))

		grpcCode, grpcOK := GRPCMapper.Map(sentinel)
		test.True(T, grpcOK)
		test.EqOp(T, codes.FailedPrecondition, grpcCode)
	}
}

func TestMappers_aMalformedRequestIsBadInput(T *testing.T) {
	T.Parallel()

	for _, sentinel := range []error{ErrEmptySubjectID, ErrUnknownRequestType} {
		code, _, ok := HTTPMapper.Map(sentinel)
		test.True(T, ok)
		test.EqOp(T, httperrors.ErrValidatingRequestInput, code)
		test.EqOp(T, http.StatusBadRequest, httperrors.HTTPStatusForCode(code))

		grpcCode, grpcOK := GRPCMapper.Map(sentinel)
		test.True(T, grpcOK)
		test.EqOp(T, codes.InvalidArgument, grpcCode)
	}
}

func TestMappers_claimNothingElse(T *testing.T) {
	T.Parallel()

	T.Run("a stranger", func(t *testing.T) {
		t.Parallel()

		stranger := platformerrors.New("something this package never returns")

		_, _, httpOK := HTTPMapper.Map(stranger)
		test.False(t, httpOK)

		grpcCode, grpcOK := GRPCMapper.Map(stranger)
		test.False(t, grpcOK)
		test.EqOp(t, codes.Unknown, grpcCode)
	})

	// nil is not an error to map, and a mapper that claimed it would turn every
	// success into whatever status it picked.
	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, _, httpOK := HTTPMapper.Map(nil)
		test.False(t, httpOK)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)

		grpcCode, grpcOK := GRPCMapper.Map(nil)
		test.False(t, grpcOK)
		test.EqOp(t, codes.OK, grpcCode)
	})
}
