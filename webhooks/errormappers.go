package webhooks

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
// switch that decides what a refused endpoint registration means on the wire
// lives beside the error that refused it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a
// side effect a consumer cannot opt out of. The composition root registers the
// domain tier, and for this module that is one call — errormappers.Register.
//
// They map more than webhooks/grpc raises, deliberately. A consumer serving
// Replay from their own handler gets ErrEndpointDisabled and ErrDeliveryNotFound
// answered too, and a mapping that only covered the nine RPCs this module ships
// would make the answer depend on which transport happened to ask.
//
// Two absences are worth naming because both look mappable and neither is.
// ErrNoSigningSecret is requestsigning.ErrNoSigningKey and ErrCircuitOpen wraps
// circuitbreaking.ErrCircuitBroken — both are a primitive's sentinel, raised in
// this module and in others, and a case here would be webhooks deciding what
// that sentinel means everywhere in the process. A keyring with no key is a
// wiring failure wherever else it is raised, so the 500 it resolves to is the
// honest answer there; webhooks/grpc refuses a keyless save at the request
// instead, where the answer is about that request rather than about the
// sentinel. The circuit breaker's is already answered by the platform mapper,
// which is the tier that owns it.
//
// internal/sentinelmatrix records which sentinel is in which of the three
// states, and fails when one is in none.
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
	// The two rows a caller can name and not reach. Both are the same answer a
	// read in another tenant's scope already gets, which is what keeps a read
	// from being an enumeration oracle over somebody else's endpoints.
	case errors.Is(err, ErrUnknownSubscription):
		return httperrors.ErrDataNotFound, "no such webhook subscription", true
	case errors.Is(err, ErrDeliveryNotFound):
		return httperrors.ErrDataNotFound, "no such webhook delivery", true

	// The registration refusals a caller corrects by sending something else,
	// each naming the field rather than the rule. The URL messages say which of
	// the two checks refused it — a scheme somebody can fix by typing https, or
	// a host that is not reachable from the internet — and neither says what the
	// name resolved to, which would be a probe of the deployment's own network.
	case errors.Is(err, ErrInvalidEndpointURL):
		return httperrors.ErrValidatingRequestInput, "the endpoint URL must be an absolute https:// address", true
	case errors.Is(err, ErrDisallowedEndpointHost):
		return httperrors.ErrValidatingRequestInput, "the endpoint host is not publicly routable", true
	case errors.Is(err, ErrReservedHeader):
		return httperrors.ErrValidatingRequestInput, "the endpoint sets a header this service reserves", true
	case errors.Is(err, ErrNoEvents):
		return httperrors.ErrValidatingRequestInput, "an endpoint must subscribe to at least one event type", true
	case errors.Is(err, ErrUnknownEventType):
		return httperrors.ErrValidatingRequestInput, "no such event type", true
	case errors.Is(err, ErrScopeMismatch):
		return httperrors.ErrValidatingRequestInput, "the endpoint does not belong to that scope", true

	// The two conflicts with state that already exists. Neither is corrected by
	// re-sending the same request, which is what separates them from the six
	// above.
	//
	// The out-of-scope message deliberately does not say that the identifier
	// exists somewhere else, though the sentinel's own name does: what a caller
	// needs is a different identifier, and what they must not learn is that
	// another tenant is holding this one.
	case errors.Is(err, ErrEndpointOutOfScope):
		return httperrors.ErrResourceConflict, "that webhook endpoint identifier is not available", true
	case errors.Is(err, ErrEndpointDisabled):
		return httperrors.ErrResourceConflict, "the webhook endpoint is disabled", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrUnknownSubscription), errors.Is(err, ErrDeliveryNotFound):
		return codes.NotFound, true

	case errors.Is(err, ErrInvalidEndpointURL), errors.Is(err, ErrDisallowedEndpointHost),
		errors.Is(err, ErrReservedHeader), errors.Is(err, ErrNoEvents),
		errors.Is(err, ErrUnknownEventType), errors.Is(err, ErrScopeMismatch):
		return codes.InvalidArgument, true

	// AlreadyExists rather than Aborted, because the collision is on a unique
	// key and that is the code gRPC reserves for exactly that. The identifier
	// was the caller's to choose, so re-sending will not help and a different
	// one will.
	case errors.Is(err, ErrEndpointOutOfScope):
		return codes.AlreadyExists, true

	// FailedPrecondition rather than InvalidArgument: the request is well formed
	// and it is the endpoint's state that refuses it, which is gRPC's own
	// distinction between the two — the client changes the system's state and
	// then retries, by enabling the endpoint.
	case errors.Is(err, ErrEndpointDisabled):
		return codes.FailedPrecondition, true
	default:
		return codes.Unknown, false
	}
}
