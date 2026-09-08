package links

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels, and the reason they are
// here rather than in errors/http and errors/grpc.
//
// Those two packages are primitives. They may know about database, ratelimiting
// and the platform sentinels, which are primitives too, and nothing above them
// — so the switch that decides an action link's status lives beside the action
// link errors, and the import runs the other way from the one this file's
// sibling errors.go used to describe.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a
// side effect a consumer cannot opt out of. The composition root registers the
// domain tier, and for this module that is one call — errormappers.Register,
// which service.Register makes for a service built from a service.Config and a
// service assembled by hand makes itself, next to the mappers it declares for
// its own sentinels.
var (
	// HTTPMapper maps this package's sentinels onto HTTP error codes.
	HTTPMapper httperrors.HTTPErrorMapper = httpMapper{}

	// GRPCMapper maps this package's sentinels onto gRPC codes. It covers the
	// same sentinels HTTPMapper does, deliberately: a service exposing both
	// transports would otherwise answer one failure with a considered status on
	// one and codes.Unknown on the other, and which one a client got would
	// depend on how it happened to connect.
	GRPCMapper grpcerrors.GRPCErrorMapper = grpcMapper{}
)

// ClientSafeSentinels are the redemption outcomes whose own text a gRPC server
// may return to a caller verbatim, handed to
// errors/grpc.RegisterClientSafeSentinels by errormappers.Register alongside the
// four mappers.
//
// The four are separate sentinels rather than one for the reason errors.go
// gives — a 256-bit token is never guessed, so naming the outcome is not an
// oracle — and that reasoning does not stop at the transport. gRPC derives its
// status message from the code, so without this a client is told
// "FailedPrecondition" for all four, which is the one thing the separation
// exists to avoid, while an HTTP client is told which. ErrInvalidToken is here
// for the same reason: "invalid action link token" is the whole of what a
// caller needs and the whole of what this package knows.
var ClientSafeSentinels = []error{
	ErrLinkNotFound,
	ErrLinkAlreadyRedeemed,
	ErrLinkExpired,
	ErrLinkRevoked,
	ErrInvalidToken,
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
	// One code for the four, and a message per outcome. The message is the half
	// a person reads, and here it can be specific without disclosing anything —
	// see httperrors.ErrActionLinkUnusable on why an action link is not a
	// session cookie in this respect.
	case errors.Is(err, ErrLinkAlreadyRedeemed):
		return httperrors.ErrActionLinkUnusable, "this link has already been used", true
	case errors.Is(err, ErrLinkExpired):
		return httperrors.ErrActionLinkUnusable, "this link has expired", true
	case errors.Is(err, ErrLinkRevoked):
		return httperrors.ErrActionLinkUnusable, "this link is no longer valid", true
	case errors.Is(err, ErrLinkNotFound):
		return httperrors.ErrActionLinkUnusable, "this link is not valid", true
	// A malformed token never named a link, so it is ordinary bad input and gets
	// a 400 rather than the 410 above.
	case errors.Is(err, ErrInvalidToken):
		return httperrors.ErrValidatingRequestInput, "invalid link", true
	default:
		return "", "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	// FailedPrecondition rather than NotFound, and the gap between the two is
	// the point. NotFound invites the client to try the same URL again; a link
	// that has been used, has expired, or has been revoked will never work
	// again, and the retry it invites is a person clicking a dead link twice.
	// FailedPrecondition's documented advice — change the state, then retry — is
	// exactly right: the state to change is "hold a live link", and the way to
	// change it is to ask for a new one.
	case errors.Is(err, ErrLinkAlreadyRedeemed),
		errors.Is(err, ErrLinkExpired),
		errors.Is(err, ErrLinkRevoked),
		errors.Is(err, ErrLinkNotFound):
		return codes.FailedPrecondition, true
	// A malformed token never named a link at all, which is ordinary bad input.
	case errors.Is(err, ErrInvalidToken):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
