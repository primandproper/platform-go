package notifications

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
// switch that decides what a refused registration or an absent notification
// means on the wire lives beside the error that refused it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a side
// effect a consumer cannot opt out of. The composition root registers the domain
// tier, and for this module that is one call — errormappers.Register.
//
// The sentinels absent from both switches are absent on purpose: the four
// nil-argument ones wrap platform sentinels the platform mappers already answer.
// internal/sentinelmatrix records which sentinel is in which state, and fails
// when one is in none.
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

// There are no ClientSafeSentinels here, which is a decision rather than an
// omission.
//
// The refusals a caller can act on each name a field a client is about to
// re-send — the topic, the token, the platform — and the message the mapper
// writes below already says which one, in words chosen for the person reading
// them. The two that are not about a field are the two not-founds, and their
// whole property is that they say as little as possible: a notification
// addressed to somebody else and a device registered to somebody else both read
// as absent, which is what keeps a read from being an oracle for what other
// people have been told and what handsets they hold. Handing gRPC the
// sentinel's own wording would add nothing to the first group and would be
// exactly the wrong instinct on the second.

type (
	httpMapper struct{}
	grpcMapper struct{}
)

func (httpMapper) Map(err error) (code httperrors.ErrorCode, msg string, ok bool) {
	if err == nil {
		return httperrors.ErrNothingSpecific, "", false
	}

	switch {
	// Absent, archived, and addressed to somebody else are one answer. That is
	// the inbox's own reading — see ErrNotificationNotFound — and it is what
	// keeps a read by id from telling a caller that a notification they cannot
	// see exists.
	case errors.Is(err, ErrNotificationNotFound):
		return httperrors.ErrDataNotFound, "no such notification", true

	// The same reading one seam over: a registration under another principal is
	// not one this caller may learn the existence of.
	case errors.Is(err, ErrDeviceNotFound):
		return httperrors.ErrDataNotFound, "no such device", true

	// The four a caller can correct, each naming its field.
	case errors.Is(err, ErrEmptyPrincipal):
		return httperrors.ErrValidatingRequestInput, "a recipient is required", true
	case errors.Is(err, ErrEmptyTopic):
		return httperrors.ErrValidatingRequestInput, "a notification topic is required", true
	case errors.Is(err, ErrEmptyToken):
		return httperrors.ErrValidatingRequestInput, "a device token is required", true
	case errors.Is(err, ErrUnknownPlatform):
		return httperrors.ErrValidatingRequestInput, "the device platform is not one this service pushes to", true

	// A write whose entity names a different scope than the write does. It is
	// refused rather than corrected, and the caller is the one holding both
	// halves.
	case errors.Is(err, ErrScopeMismatch):
		return httperrors.ErrValidatingRequestInput, "the entity does not belong to that scope", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	// One answer for absent, archived and somebody else's, on both seams, for
	// the reason the HTTP mapper gives above.
	case errors.Is(err, ErrNotificationNotFound), errors.Is(err, ErrDeviceNotFound):
		return codes.NotFound, true

	// The five a caller can correct. They share a code and differ in their
	// message, which is why errors/grpc's encoding of the chain — and the
	// decoding interceptor on this module's clients — is what a client actually
	// branches on.
	case errors.Is(err, ErrEmptyPrincipal), errors.Is(err, ErrEmptyTopic),
		errors.Is(err, ErrEmptyToken), errors.Is(err, ErrUnknownPlatform),
		errors.Is(err, ErrScopeMismatch):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
