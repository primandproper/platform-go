package passwordreset

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels, and the reason they are
// here rather than in errors/http and errors/grpc.
//
// Those two are primitives. They may know about database, ratelimiting and the
// platform sentinels, which are primitives too, and nothing above them — so the
// switch that decides what a spent reset link means on the wire lives beside the
// error that refused it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a side
// effect a consumer cannot opt out of. The composition root registers the domain
// tier, and for this module that is one call — errormappers.Register.
//
// The sentinels absent from both switches are absent on purpose. Four of them
// wrap a platform sentinel the platform mappers already answer — two nil
// arguments and two empty ones — and a case here would be a second copy of a
// decision already made, free to drift from it. The fifth,
// ErrNonPositiveLifetime, is answered by nobody: a lifetime of zero is an unset
// configuration field read at issuance, so it reaches a client only through a
// service that shipped broken and a 500 is the honest reply.
// internal/sentinelmatrix records which sentinel is in which state, and fails
// when one is in neither.
var (
	// HTTPMapper maps this package's sentinels onto HTTP error codes.
	HTTPMapper httperrors.HTTPErrorMapper = httpMapper{}

	// GRPCMapper maps this package's sentinels onto gRPC codes. It covers the
	// same sentinels HTTPMapper does, deliberately: a service exposing both
	// transports would otherwise answer one refusal with a considered status on
	// one and codes.Unknown on the other, and which one a client got would
	// depend on how it happened to connect.
	GRPCMapper grpcerrors.GRPCErrorMapper = grpcMapper{}
)

// ClientSafeSentinels are the three redemption outcomes whose own text a gRPC
// server may return to a caller verbatim, handed to
// errors/grpc.RegisterClientSafeSentinels by errormappers.Register alongside the
// mappers.
//
// They are the three the package documentation argues a person is owed, and the
// argument does not stop at the transport. All three carry one gRPC code, so
// without this list a client is told "FailedPrecondition" for a link that has
// expired, one that has been used and one that was never issued — which is
// exactly the distinction the three separate sentinels exist to draw.
//
// The disclosure is the same one Store.Verify and Store.Consume already make,
// and it is bounded by the same fact: the secret is high-entropy, so learning
// which of the three happened requires already holding the token. Telling
// somebody with a day-old link that it expired is worth more than the nothing an
// attacker learns from it.
var ClientSafeSentinels = []error{
	ErrTokenNotFound,
	ErrTokenExpired,
	ErrTokenRedeemed,
}

type (
	httpMapper struct{}
	grpcMapper struct{}
)

func (httpMapper) Map(err error) (code httperrors.ErrorCode, msg string, ok bool) {
	if err == nil {
		return httperrors.ErrNothingSpecific, "", false
	}

	switch {
	// One code for the three, and a message per outcome, which is the shape
	// httperrors.ErrActionLinkUnusable was written for — its own documentation
	// names reset links among the four kinds it covers. The 410 it carries is
	// the half a client must not get wrong: none of these three gets better on a
	// second request, and a 404 would invite somebody to click a dead link again.
	//
	// The messages are the sentinels' own rather than a paraphrase. They are
	// written for the person reading them, which is why the same three are
	// ClientSafeSentinels above, and a paraphrase here with the sentinel's
	// wording on the gRPC side would be one refusal with two texts.
	case errors.Is(err, ErrTokenRedeemed):
		return httperrors.ErrActionLinkUnusable, ErrTokenRedeemed.Error(), true
	case errors.Is(err, ErrTokenExpired):
		return httperrors.ErrActionLinkUnusable, ErrTokenExpired.Error(), true
	case errors.Is(err, ErrTokenNotFound):
		return httperrors.ErrActionLinkUnusable, ErrTokenNotFound.Error(), true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	// FailedPrecondition rather than NotFound for all three, including the one
	// that is an absence. NotFound invites the client to try the same request
	// again; a token that was never issued, has expired, or has been spent will
	// never verify, and the state to change is "hold a live link" — which is
	// asked for by starting the reset again rather than by retrying this call.
	case errors.Is(err, ErrTokenNotFound),
		errors.Is(err, ErrTokenExpired),
		errors.Is(err, ErrTokenRedeemed):
		return codes.FailedPrecondition, true
	default:
		return codes.Unknown, false
	}
}
