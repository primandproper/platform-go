package sessions

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
// gives: a service exposing both would otherwise answer an expired session with
// a considered 401 on one and codes.Unknown on the other, and which one a client
// got would depend on how it happened to connect.
//
// The rest of this package's sentinels are construction-time or backend-shaped —
// a policy with no timeout, a backend that keeps no principal index — and reach
// a client only through a service that shipped broken, where a 500 is the honest
// answer. internal/sentinelmatrix is where that split is written down and
// checked against this file.
var mappedSentinels = []error{
	ErrNotFound,
	ErrExpired,
	ErrIdleTimeout,
	ErrAbsoluteTimeout,
}

func TestMappers_coverTheSameSentinels(T *testing.T) {
	T.Parallel()

	for _, sentinel := range mappedSentinels {
		T.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()

			code, msg, httpOK := HTTPMapper.Map(sentinel)
			test.True(t, httpOK, test.Sprintf("no HTTP mapping for %v", sentinel))
			test.EqOp(t, httperrors.ErrFetchingSessionContextData, code)
			test.NotEq(t, "", msg)
			// Every unusable session is a 401 and a message that says nothing
			// about which kind: telling a client apart "no such session" from
			// "that one expired" is an oracle for whether a guessed identifier
			// ever existed.
			test.EqOp(t, http.StatusUnauthorized, httperrors.HTTPStatusForCode(code))

			grpcCode, grpcOK := GRPCMapper.Map(sentinel)
			test.True(t, grpcOK, test.Sprintf("no gRPC mapping for %v", sentinel))
			test.EqOp(t, codes.Unauthenticated, grpcCode)
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

			wrapped := platformerrors.Wrap(sentinel, "reading session")

			_, _, httpOK := HTTPMapper.Map(wrapped)
			test.True(t, httpOK)

			_, grpcOK := GRPCMapper.Map(wrapped)
			test.True(t, grpcOK)
		})
	}
}

// ErrExpired wraps ErrNotFound, so ordering decides which message wins — and the
// more specific one has to.
func TestHTTPMapper_expiredIsReportedAsExpired(T *testing.T) {
	T.Parallel()

	_, msg, ok := HTTPMapper.Map(ErrIdleTimeout)
	test.True(T, ok)
	test.EqOp(T, "session expired", msg)

	_, msg, ok = HTTPMapper.Map(ErrNotFound)
	test.True(T, ok)
	test.EqOp(T, "no active session", msg)
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
