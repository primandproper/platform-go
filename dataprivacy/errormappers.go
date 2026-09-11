package dataprivacy

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels, and the reason they are
// here rather than in errors/http and errors/grpc.
//
// Those two packages are primitives. They may know about database, ratelimiting
// and the platform sentinels, which are primitives too, and nothing above them
// — so the switch that decides a privacy request's status lives beside the
// privacy request errors, and the import runs the other way.
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

type (
	httpMapper struct{}
	grpcMapper struct{}
)

func (httpMapper) Map(err error) (code httperrors.ErrorCode, msg string, ok bool) {
	if err == nil {
		return httperrors.ErrNothingSpecific, "", false
	}

	switch {
	// A subject asking after their own export or erasure is a client, and
	// "request not found" reaching them as a 500 tells them the service is
	// broken when the answer is that the ID is not one of theirs.
	case errors.Is(err, ErrRequestNotFound):
		return httperrors.ErrDataNotFound, "privacy request not found", true
	// A conflict rather than a not-found: the request exists and the caller may
	// see it, it is simply not in the state the call needs. The remedy is a state
	// change somebody else makes — a confirmation arrives, an export finishes —
	// not a corrected request.
	case errors.Is(err, ErrNotAwaitingConfirmation):
		return httperrors.ErrResourceConflict, "privacy request is not awaiting confirmation", true
	case errors.Is(err, ErrArtifactUnavailable):
		return httperrors.ErrResourceConflict, "privacy request has no downloadable artifact", true
	case errors.Is(err, ErrEmptySubjectID):
		return httperrors.ErrValidatingRequestInput, "privacy request requires a subject", true
	case errors.Is(err, ErrGlobalRequestScope):
		return httperrors.ErrValidatingRequestInput, "privacy request cannot be confined to the global scope", true
	case errors.Is(err, ErrUnknownRequestType):
		return httperrors.ErrValidatingRequestInput, "unknown privacy request type", true
	default:
		return "", "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrRequestNotFound):
		return codes.NotFound, true
	// FailedPrecondition for both: the request exists and the caller may see it,
	// but it is not in the state the call needs. The state has to change —
	// somebody confirms the request, or the export finishes — before a retry can
	// succeed, which is exactly what FailedPrecondition tells a client.
	case errors.Is(err, ErrNotAwaitingConfirmation),
		errors.Is(err, ErrArtifactUnavailable):
		return codes.FailedPrecondition, true
	case errors.Is(err, ErrEmptySubjectID),
		errors.Is(err, ErrGlobalRequestScope),
		errors.Is(err, ErrUnknownRequestType):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
