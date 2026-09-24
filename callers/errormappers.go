package callers

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels.
//
// They import two primitives and nothing from this module, so this package stays
// the leaf its own tests pin.
//
// # Why one sentinel is claimed and the other is not
//
// ErrNoPrincipal is claimed because the code a surface falls back to is not
// written for it. audit/grpc answers an unresolvable scope InvalidArgument and
// mediaregistry/http answers an unresolvable caller 500 — both right for a
// consumer's resolver that could not place a request, both wrong for a request
// with nobody on it — and a mapper is what outranks a fallback. Every surface
// that reads a principal itself already answers its absence Unauthenticated at
// the call site, so this makes the derived seams say what those do.
//
// ErrTargetNotPermitted is not, for the reason on it: every surface that raises
// it has already chosen a status at the call site, and several choose an absence
// on purpose.
//
// Nothing registers these on its own. errormappers.Register is the one call.
var (
	// HTTPMapper maps this package's sentinels onto HTTP error codes.
	HTTPMapper httperrors.HTTPErrorMapper = httpMapper{}

	// GRPCMapper maps this package's sentinels onto gRPC codes, covering the same
	// sentinels HTTPMapper does.
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
	// E101 rather than E123: this is a request carrying no session this service
	// could read, not a sign-in that failed.
	case errors.Is(err, ErrNoPrincipal):
		return httperrors.ErrFetchingSessionContextData, "authentication required", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrNoPrincipal):
		return codes.Unauthenticated, true
	default:
		return codes.Unknown, false
	}
}
