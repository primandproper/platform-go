package grants

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels. errors/http and
// errors/grpc are primitives and cannot import the tier above them, so the
// decision about what a grant refusal means on the wire lives beside the
// refusal. Nothing registers these on its own: the composition root does, in
// one call — errormappers.Register.
//
// The endpoints these are for are the consumer's own: the callback that stores a
// consent, the settings page that lists or disconnects an account, and the
// handler that syncs through one and meets a grant the provider has let go of.
// This package ships no transport of its own.
//
// The nil arguments, ErrValueTooLong and ErrUnknownRevocationReason are absent
// from both switches on purpose: they wrap a platform sentinel the platform
// mappers already answer. So is ErrProviderReturnedNoAccessToken, for a
// different reason: it is the provider misbehaving, which the caller of the
// consumer's endpoint neither caused nor can fix, and a 500 says exactly that.
// internal/sentinelmatrix records which sentinel is in which state.
var (
	// HTTPMapper maps this package's sentinels onto HTTP error codes.
	HTTPMapper httperrors.HTTPErrorMapper = httpMapper{}

	// GRPCMapper maps this package's sentinels onto gRPC codes, covering the
	// same sentinels HTTPMapper does.
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
	// No live grant: never connected, disconnected, revoked by the provider, or
	// somebody else's tenant. They read the same so the answer is not an
	// oracle for which accounts are connected elsewhere.
	case errors.Is(err, ErrGrantNotFound):
		return httperrors.ErrDataNotFound, "no connected account for that provider", true

	// Lost a refresh race. The caller reads again and carries on; a 500 would
	// tell it the grant was broken.
	case errors.Is(err, ErrStaleRefresh):
		return httperrors.ErrResourceConflict, "the connected account was refreshed concurrently; read it again", true

	// The two that no retry fixes and a new consent does. The message says what
	// the person has to do, which is the one thing both have in common.
	case errors.Is(err, ErrProviderRevoked),
		errors.Is(err, ErrNoRefreshToken):
		return httperrors.ErrResourceConflict, "the connected account has to be connected again", true

	case errors.Is(err, ErrEmptySubject):
		return httperrors.ErrValidatingRequestInput, "a grant needs a subject", true
	case errors.Is(err, ErrEmptyProvider):
		return httperrors.ErrValidatingRequestInput, "a grant needs a provider", true
	case errors.Is(err, ErrEmptyAccessToken):
		return httperrors.ErrValidatingRequestInput, "a grant needs an access token", true
	case errors.Is(err, ErrInvalidGrantedScope):
		return httperrors.ErrValidatingRequestInput, "a granted scope is not an OAuth2 scope token", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrGrantNotFound):
		return codes.NotFound, true

	// Aborted is gRPC's code for a concurrency conflict a client retries at a
	// higher level — read, then write again — which is exactly the loser's move.
	case errors.Is(err, ErrStaleRefresh):
		return codes.Aborted, true

	// FailedPrecondition: the system is not in a state the call can succeed in,
	// and it will not be until something else — a new consent — happens.
	case errors.Is(err, ErrProviderRevoked),
		errors.Is(err, ErrNoRefreshToken):
		return codes.FailedPrecondition, true

	case errors.Is(err, ErrEmptySubject),
		errors.Is(err, ErrEmptyProvider),
		errors.Is(err, ErrEmptyAccessToken),
		errors.Is(err, ErrInvalidGrantedScope):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
