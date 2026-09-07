package errors_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/circuitbreaking"
	"github.com/primandproper/platform-go/v14/cryptography/requestsigning"
	"github.com/primandproper/platform-go/v14/database"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	grpcerrors "github.com/primandproper/platform-go/v14/errors/grpc"
	httperrors "github.com/primandproper/platform-go/v14/errors/http"
	"github.com/primandproper/platform-go/v14/idempotency"
	"github.com/primandproper/platform-go/v14/ratelimiting"
	textsearch "github.com/primandproper/platform-go/v14/search/text"
	vectorsearch "github.com/primandproper/platform-go/v14/search/vector"

	"github.com/shoenig/test"
	"google.golang.org/grpc/codes"
)

// mappedSentinels is every platform sentinel both transports are expected to
// have an answer for.
//
// It is a single list rather than one per mapper on purpose. The two drifted
// apart once already — gRPC had no case for sessions or operations, so an
// expired session reached an HTTP client as a considered 401 and a gRPC client
// as codes.Unknown — and a per-mapper list is exactly the shape that lets it
// happen again without anybody noticing.
//
// Adding a sentinel here and to only one mapper fails this test, which is the
// point. A sentinel that genuinely belongs on one transport and not the other
// does not go in this list; it goes in that mapper's own test, with a comment
// saying why.
//
// It is the platform tier only, and that is the same boundary the two mappers
// hold: dataprivacy, identity, links, operations and sessions map their own
// sentinels now, so the same parity is asserted over each of their two mappers in
// their own package, and over all five at once by internal/sentinelmatrix.
var mappedSentinels = []error{
	database.ErrUserAlreadyExists,
	circuitbreaking.ErrCircuitBroken,
	platformerrors.ErrPermissionDenied,
	platformerrors.ErrResourceInUse,
	platformerrors.ErrQuotaExhausted,
	platformerrors.ErrNotEntitled,
	platformerrors.ErrNilInputParameter,
	platformerrors.ErrEmptyInputParameter,
	platformerrors.ErrInvalidIDProvided,
	platformerrors.ErrEmptyInputProvided,
	platformerrors.ErrUnrecognizedInputValue,
	ratelimiting.ErrRateLimited,
	requestsigning.ErrStaleSignature,
	requestsigning.ErrInvalidSignature,
	idempotency.ErrInFlight,
	idempotency.ErrFingerprintMismatch,
	idempotency.ErrKeyRequired,
	idempotency.ErrKeyTooLong,
	idempotency.ErrKeyInvalid,
	textsearch.ErrInvalidCursor,
	textsearch.ErrEmptyQueryProvided,
	textsearch.ErrResultWindowExceeded,
	vectorsearch.ErrNotFound,
	vectorsearch.ErrEmptyEmbedding,
	vectorsearch.ErrDimensionMismatch,
}

func TestPlatformMappers_coverTheSameSentinels(T *testing.T) {
	T.Parallel()

	for _, sentinel := range mappedSentinels {
		T.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()

			code, msg, httpOK := httperrors.PlatformMapper.Map(sentinel)
			test.True(t, httpOK, test.Sprintf("no HTTP mapping for %v", sentinel))
			test.NotEq(t, httperrors.ErrorCode(""), code)
			test.NotEq(t, "", msg)

			grpcCode, grpcOK := grpcerrors.PlatformMapper.Map(sentinel)
			test.True(t, grpcOK, test.Sprintf("no gRPC mapping for %v", sentinel))
			test.NotEqOp(t, codes.Unknown, grpcCode)
		})
	}
}

func TestPlatformMappers_wrappedSentinelsStillMap(T *testing.T) {
	T.Parallel()

	// The mappers are reached from handlers, which wrap. A mapping that only
	// works on a bare sentinel works nowhere real.
	for _, sentinel := range mappedSentinels {
		T.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()

			wrapped := platformerrors.Wrap(sentinel, "doing the thing")

			_, _, httpOK := httperrors.PlatformMapper.Map(wrapped)
			test.True(t, httpOK)

			_, grpcOK := grpcerrors.PlatformMapper.Map(wrapped)
			test.True(t, grpcOK)
		})
	}
}

func TestPlatformMappers_unknownErrorsAreNotClaimed(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		stranger := platformerrors.New("something nobody has a mapping for")

		_, _, httpOK := httperrors.PlatformMapper.Map(stranger)
		test.False(t, httpOK)

		grpcCode, grpcOK := grpcerrors.PlatformMapper.Map(stranger)
		test.False(t, grpcOK)
		test.EqOp(t, codes.Unknown, grpcCode)
	})
}
