package operations

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
// — so the switch that decides an operation's status lives beside the operation
// errors, and the import runs the other way from the one this file's sibling
// errors.go used to describe.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a
// side effect a consumer cannot opt out of. The composition root registers the
// domain tier, and for this module that is one call — errormappers.Register,
// which service.Register makes for a service built from a service.Config and a
// service assembled by hand makes itself, next to the mappers it declares for
// its own sentinels.
//
// operations/http.New registers the HTTP one a second time, and is the only
// place in this module that registers anything for itself. It can be because it
// was the only surface here that answered a request through errors/http and
// belonged to a package in that list, so constructing it is already the
// statement that this process serves operation errors on the wire.
// dataprivacy/http is a second such surface now and registers nothing — one door
// stays one door. The registries are append-only and stop at the first match, so
// the second copy answers identically and is never reached.
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
	// An operation nobody may read and an operation that does not exist are the
	// same answer on purpose. The read paths resolve an owner and return
	// ErrOperationNotFound for a row belonging to somebody else, so that an ID
	// somebody guessed cannot be confirmed as real by the status it comes back
	// with.
	case errors.Is(err, ErrOperationNotFound):
		return httperrors.ErrDataNotFound, "operation not found", true
	// A subscription refused for capacity is a retry-later, not a failure of the
	// request: the same subscription will be accepted when somebody disconnects.
	case errors.Is(err, ErrTooManyWatchers):
		return httperrors.ErrTooManyRequests, "too many concurrent operation subscriptions", true
	default:
		return "", "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	// An operation nobody may read and an operation that does not exist are the
	// same answer, for the same reason HTTPMapper gives.
	case errors.Is(err, ErrOperationNotFound):
		return codes.NotFound, true
	// ResourceExhausted rather than Unavailable: nothing is down, the fleet is
	// simply at its subscription ceiling, and the client should back off and
	// retry rather than fail over to an instance with the same ceiling.
	case errors.Is(err, ErrTooManyWatchers):
		return codes.ResourceExhausted, true
	default:
		return codes.Unknown, false
	}
}
