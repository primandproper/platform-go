package links

import (
	"net/http"
	"testing"

	platformerrors "github.com/primandproper/primitives-go/errors"
	httperrors "github.com/primandproper/primitives-go/errors/http"

	"github.com/shoenig/test"
	"google.golang.org/grpc/codes"
)

// mappedSentinels is every sentinel in this package both mappers are expected to
// have an answer for: the four redemption outcomes and the malformed token.
//
// One list rather than one per transport, for the reason errors' own parity test
// gives: a service exposing both would otherwise answer an expired link with a
// considered 410 on one and codes.Unknown on the other, and which one a client
// got would depend on how it happened to connect.
//
// The rest of this package's sentinels are raised while wiring a Minter up or
// are the store reporting itself, and reach a client only through a service that
// shipped broken or a dependency that is down. internal/sentinelmatrix is where
// that split is written down and checked against this file.
var mappedSentinels = []error{
	ErrLinkNotFound,
	ErrLinkAlreadyRedeemed,
	ErrLinkExpired,
	ErrLinkRevoked,
	ErrInvalidToken,
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

	// The Minter wraps its sentinels on the way out, so the mappers have to
	// match through the wrapping rather than on identity.
	for _, sentinel := range mappedSentinels {
		T.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()

			wrapped := platformerrors.Wrap(sentinel, "redeeming action link")

			_, _, httpOK := HTTPMapper.Map(wrapped)
			test.True(t, httpOK)

			_, grpcOK := GRPCMapper.Map(wrapped)
			test.True(t, grpcOK)
		})
	}
}

func TestHTTPMapper_everyUnusableLinkIsOneCodeAndAGone(T *testing.T) {
	T.Parallel()

	for err, expected := range map[error]string{
		ErrLinkAlreadyRedeemed: "this link has already been used",
		ErrLinkExpired:         "this link has expired",
		ErrLinkRevoked:         "this link is no longer valid",
		ErrLinkNotFound:        "this link is not valid",
	} {
		code, msg, ok := HTTPMapper.Map(err)
		test.True(T, ok)
		test.EqOp(T, httperrors.ErrActionLinkUnusable, code)
		test.EqOp(T, http.StatusGone, httperrors.HTTPStatusForCode(code))
		// One code, four messages: the distinction that matters is the one a
		// person reads, not one a client branches on.
		test.EqOp(T, expected, msg)
	}
}

// A malformed token never named a link, so it is bad input rather than a link
// outcome, and gets a 400 instead of the 410 above.
func TestMappers_invalidTokenIsOrdinaryBadInput(T *testing.T) {
	T.Parallel()

	code, _, ok := HTTPMapper.Map(ErrInvalidToken)
	test.True(T, ok)
	test.EqOp(T, httperrors.ErrValidatingRequestInput, code)
	test.EqOp(T, http.StatusBadRequest, httperrors.HTTPStatusForCode(code))

	grpcCode, grpcOK := GRPCMapper.Map(ErrInvalidToken)
	test.True(T, grpcOK)
	test.EqOp(T, codes.InvalidArgument, grpcCode)
}

// FailedPrecondition rather than NotFound: a link that has been used, has
// expired, or has been revoked will never work again, and NotFound invites the
// client to retry the URL that just failed.
func TestGRPCMapper_everyUnusableLinkIsAFailedPrecondition(T *testing.T) {
	T.Parallel()

	for _, err := range []error{ErrLinkAlreadyRedeemed, ErrLinkExpired, ErrLinkRevoked, ErrLinkNotFound} {
		code, ok := GRPCMapper.Map(err)
		test.True(T, ok)
		test.EqOp(T, codes.FailedPrecondition, code)
	}
}

// The four redemption outcomes share one gRPC code, so the message is the only
// place the difference survives — which is what ClientSafeSentinels is for, and
// why the four have to be four distinct sentences.
func TestClientSafeSentinels_doNotCollapseIntoOneMessage(T *testing.T) {
	T.Parallel()

	seen := map[string]struct{}{}
	for _, sentinel := range ClientSafeSentinels {
		seen[sentinel.Error()] = struct{}{}
	}

	test.MapLen(T, len(ClientSafeSentinels), seen)

	// Every one of them is a sentinel this package also maps: a message on the
	// wire for an error with no considered status would arrive beside
	// codes.Unknown.
	for _, sentinel := range ClientSafeSentinels {
		_, ok := GRPCMapper.Map(sentinel)
		test.True(T, ok, test.Sprintf("%v is client-safe and unmapped", sentinel))
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
