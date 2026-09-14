package metering

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
// switch that decides what a refused usage record means on the wire lives beside
// the error that refused it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a side
// effect a consumer cannot opt out of. The composition root registers the domain
// tier, and for this module that is one call — errormappers.Register.
//
// What the two switches claim is the ingest path and nothing else. Usage arrives
// over a consumer's own endpoint — an HTTP handler behind a client that retries,
// a queue consumer that redelivers — and Usage.validate is the boundary it
// crosses, so the five refusals it raises and the one the registry raises for a
// meter nobody declared are the six a client can produce and the six that get a
// status.
//
// Everything else is deliberately unclaimed, and internal/sentinelmatrix records
// which sentinel is in which state. Seven wrap a platform sentinel the platform
// mappers already answer, and a case here would be a second copy of a decision
// already made, free to drift from it. The remaining ten are raised while the
// component is wired up — two registrations under one name, a quota over a window
// its meter does not bucket by, a plan limits table that cannot be served, a
// billing period nothing can resolve — or inside the flusher's own goroutine,
// where no client is waiting. A request that failed because the service was built
// wrong is a 500, honestly.
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

type (
	httpMapper struct{}
	grpcMapper struct{}
)

func (httpMapper) Map(err error) (code httperrors.ErrorCode, msg string, ok bool) {
	if err == nil {
		return httperrors.ErrNothingSpecific, "", false
	}

	switch {
	// The five a usage record is refused for, each message naming the field to
	// go back to. They share one code because they are one thing — the record
	// this caller sent cannot be ingested as written — and a client that wants
	// to branch matches the sentinel, which survives the wire either way.
	case errors.Is(err, ErrEmptySubject):
		return httperrors.ErrValidatingRequestInput, "usage must name the subject it belongs to", true
	case errors.Is(err, ErrInvalidMeterName):
		return httperrors.ErrValidatingRequestInput, "usage must name a meter, and the name must be a plain identifier", true
	case errors.Is(err, ErrEmptyIdempotencyKey):
		return httperrors.ErrValidatingRequestInput, "usage must carry an idempotency key", true
	// Distinct from the empty key because the fix differs: one caller forgot to
	// send a key and the other is deriving one from something too long to store
	// and needs to hash it instead. See ErrIdempotencyKeyTooLong.
	case errors.Is(err, ErrIdempotencyKeyTooLong):
		return httperrors.ErrValidatingRequestInput, "usage idempotency key is too long", true
	// Not a conflict and not a quota answer. Negative usage is a credit, and a
	// credit is issued at the billing provider rather than metered here — see
	// ErrNegativeQuantity.
	case errors.Is(err, ErrNegativeQuantity):
		return httperrors.ErrValidatingRequestInput, "usage quantity must not be negative", true

	// A meter nobody registered. It is bad input rather than an absence: a 404
	// would describe a resource this API does not have, and the thing that is
	// missing is a declaration in the service's own registry that the caller
	// named wrongly. The message does not say which meters exist, because
	// enumerating a service's meters is not what a rejected ingest is for.
	case errors.Is(err, ErrUnknownMeter):
		return httperrors.ErrValidatingRequestInput, "no such meter", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	// InvalidArgument for all six, which is gRPC's own line: an argument that is
	// bad regardless of the system's state. Every one of them is — a record with
	// no subject, no key, a key too long, a negative quantity, a meter name that
	// is not an identifier, or a meter name nothing declares — and none of them
	// becomes valid by waiting or by retrying.
	case errors.Is(err, ErrEmptySubject),
		errors.Is(err, ErrInvalidMeterName),
		errors.Is(err, ErrEmptyIdempotencyKey),
		errors.Is(err, ErrIdempotencyKeyTooLong),
		errors.Is(err, ErrNegativeQuantity),
		errors.Is(err, ErrUnknownMeter):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
