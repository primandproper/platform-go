package operations

import (
	"net/http"
	"testing"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"github.com/shoenig/test"
	"google.golang.org/grpc/codes"
)

// mappedSentinels is every sentinel in this package both mappers are expected to
// have an answer for.
//
// One list rather than one per transport, for the reason errors' own parity test
// gives: a service exposing both would otherwise answer a missing operation with
// a considered 404 on one and codes.Unknown on the other, and which one a client
// got would depend on how it happened to connect.
//
// The rest of this package's sentinels are wiring failures and worker-side
// outcomes — a duplicate kind, a runner that panicked, a result too large to
// record — and reach a client only through a service that shipped broken, where
// a 500 is the honest answer. internal/sentinelmatrix is where that split is
// written down and checked against this file.
var mappedSentinels = []error{
	ErrOperationNotFound,
	ErrTooManyWatchers,
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

			wrapped := platformerrors.Wrap(sentinel, "reading operation")

			_, _, httpOK := HTTPMapper.Map(wrapped)
			test.True(t, httpOK)

			_, grpcOK := GRPCMapper.Map(wrapped)
			test.True(t, grpcOK)
		})
	}
}

// An operation nobody may read and an operation that does not exist are the same
// answer on purpose, so the status has to be the one a genuinely missing row
// would produce.
func TestMappers_aMissingOperationIsANotFound(T *testing.T) {
	T.Parallel()

	code, _, ok := HTTPMapper.Map(ErrOperationNotFound)
	test.True(T, ok)
	test.EqOp(T, httperrors.ErrDataNotFound, code)
	test.EqOp(T, http.StatusNotFound, httperrors.HTTPStatusForCode(code))

	grpcCode, grpcOK := GRPCMapper.Map(ErrOperationNotFound)
	test.True(T, grpcOK)
	test.EqOp(T, codes.NotFound, grpcCode)
}

// A subscription refused for capacity is a retry-later, not a failure of the
// request: the same subscription will be accepted when somebody disconnects.
func TestMappers_tooManyWatchersIsARetryLater(T *testing.T) {
	T.Parallel()

	code, _, ok := HTTPMapper.Map(ErrTooManyWatchers)
	test.True(T, ok)
	test.EqOp(T, httperrors.ErrTooManyRequests, code)
	test.EqOp(T, http.StatusTooManyRequests, httperrors.HTTPStatusForCode(code))

	grpcCode, grpcOK := GRPCMapper.Map(ErrTooManyWatchers)
	test.True(T, grpcOK)
	test.EqOp(T, codes.ResourceExhausted, grpcCode)
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
