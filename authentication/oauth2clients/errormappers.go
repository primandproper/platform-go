package oauth2clients

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
// switch that decides what a refused registration means on the wire lives beside
// the error that refused it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a side
// effect a consumer cannot opt out of. The composition root registers the domain
// tier, and for this module that is one call — errormappers.Register.
//
// The sentinels absent from both switches are absent on purpose. The
// nil-argument and empty-argument ones wrap platform sentinels the platform
// mappers already answer, and ErrSecretGeneration is this process's own
// randomness failing, so a 500 is the honest answer. internal/sentinelmatrix
// records which sentinel is in which of the three states, and fails when one is
// in none.
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

// ClientSafeSentinels are the sentinels whose own text a gRPC server may return
// to a caller verbatim, handed to errors/grpc.RegisterClientSafeSentinels by
// errormappers.Register alongside the mappers.
//
// They are the two an authorization request meets, and only those two. Both are
// PermissionDenied and so are indistinguishable by code, both are written in the
// second person for somebody staring at a browser rather than for a log, and
// each has a different remedy: one says this application is not registered where
// you are, the other says it is not yours. Without this a person is told
// "PermissionDenied" for both and their administrator has to guess which.
//
// Neither names a registry, a person, or another client.
var ClientSafeSentinels = []error{
	ErrClientScopeMismatch,
	ErrClientOwnerMismatch,
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
	// Absent, archived, and in another registry are one answer, which is what
	// keeps the read from being an enumeration oracle over other tenants'
	// registrations.
	case errors.Is(err, ErrClientNotFound):
		return httperrors.ErrDataNotFound, "no such oauth2 client", true

	// The one refusal that means "try again" rather than "send something else":
	// the identifier was minted from crypto/rand and the caller never chose it.
	case errors.Is(err, ErrClientIDTaken):
		return httperrors.ErrResourceConflict, "could not allocate a client identifier; retry", true

	// The four the caller can correct, each naming the field.
	case errors.Is(err, ErrEmptyName):
		return httperrors.ErrValidatingRequestInput, "a client name is required", true
	case errors.Is(err, ErrNoRedirectURIs):
		return httperrors.ErrValidatingRequestInput, "at least one redirect URI is required", true
	case errors.Is(err, ErrInvalidRedirectURI):
		return httperrors.ErrValidatingRequestInput, "a redirect URI is not valid", true
	case errors.Is(err, ErrScopeMismatch):
		return httperrors.ErrValidatingRequestInput, "the client does not belong to that scope", true

	// The two refusals on authority that a person meets in a browser. Both are
	// 403: the caller is who they say they are and this registration is not
	// theirs to authorize through.
	case errors.Is(err, ErrClientScopeMismatch):
		return httperrors.ErrUserIsNotAuthorized, ErrClientScopeMismatch.Error(), true
	case errors.Is(err, ErrClientOwnerMismatch):
		return httperrors.ErrUserIsNotAuthorized, ErrClientOwnerMismatch.Error(), true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	// One answer for absent, archived and in another registry, for the reason
	// the HTTP mapper gives above.
	case errors.Is(err, ErrClientNotFound):
		return codes.NotFound, true

	// AlreadyExists rather than Aborted, because the collision is on a unique
	// key and that is the code gRPC reserves for exactly that.
	case errors.Is(err, ErrClientIDTaken):
		return codes.AlreadyExists, true

	case errors.Is(err, ErrEmptyName), errors.Is(err, ErrNoRedirectURIs),
		errors.Is(err, ErrInvalidRedirectURI), errors.Is(err, ErrScopeMismatch):
		return codes.InvalidArgument, true

	// PermissionDenied rather than Unauthenticated: the caller has proven who
	// they are in both cases, and it is the registration that is not theirs. A
	// client library treats the two differently, and answering Unauthenticated
	// would send a give-up path down the retry-with-credentials branch.
	//
	// Disclosing that the registration exists is the point here, unlike the
	// read above: these two are what a person is told after they have signed
	// in, about a client whose name they are looking at.
	case errors.Is(err, ErrClientScopeMismatch), errors.Is(err, ErrClientOwnerMismatch):
		return codes.PermissionDenied, true
	default:
		return codes.Unknown, false
	}
}
