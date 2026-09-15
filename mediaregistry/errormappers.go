package mediaregistry

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
// switch that decides what a key already in the bucket means on the wire lives
// beside the error that refused it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a side
// effect a consumer cannot opt out of. The composition root registers the domain
// tier, and for this module that is one call — errormappers.Register.
//
// # Which endpoint these are for
//
// Not mediaregistry/http, which is the guarded serve and answers its own 404
// before any encoding happens — that package's documentation says so and still
// does. The endpoint these six are for is the consumer's own upload handler,
// over the consumer's own form, because the key, the owner and the subject an
// object hangs off are all theirs; StoreAndRecord is the line at the end of it,
// and every one of these is a thing that line can tell them. Without a pair here
// a collided object key reaches that handler's client as a 500.
//
// The five nil arguments are absent from both switches on purpose: they wrap a
// platform sentinel the platform mappers already answer, and a case here would be
// a second copy of a decision already made, free to drift from it.
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

type (
	httpMapper struct{}
	grpcMapper struct{}
)

func (httpMapper) Map(err error) (code httperrors.ErrorCode, msg string, ok bool) {
	if err == nil {
		return httperrors.ErrNothingSpecific, "", false
	}

	switch {
	// The absence. An object that never existed and one in another tenant read
	// the same way, which is what keeps a keyed read from being an oracle for
	// which keys are real in other scopes — the same reasoning
	// mediaregistry/http applies to the serve route.
	//
	// Code and message both match what that route writes for itself. It decides
	// its own status before any encoding happens, deliberately, so that what a
	// client is told does not depend on whether a composition root called
	// errormappers.Register — and the two saying different things about one
	// missing object is the failure that arrangement would otherwise buy.
	case errors.Is(err, ErrObjectNotFound):
		return httperrors.ErrDataNotFound, ErrObjectNotFound.Error(), true

	// A key already registered in this scope, which is a conflict rather than a
	// server fault: the caller mints a new key and tries again, and a 500 would
	// tell them to retry the collision instead. The message says nothing about
	// who holds it.
	case errors.Is(err, ErrObjectKeyTaken):
		return httperrors.ErrResourceConflict, "that object key is already registered", true

	// A key the bucket already holds, which is the same conflict from the
	// caller's side — mint another key — and a different fact underneath. It
	// gets its own wording because a deployment reading a client's report needs
	// to know which store refused, and because on a shared bucket this is the
	// one that arrives with no row to explain it. The message says nothing about
	// who put the bytes there, for the same reason the one above says nothing
	// about who registered the key.
	case errors.Is(err, ErrObjectKeyOccupied):
		return httperrors.ErrResourceConflict, "that object key is already in use", true

	// The two ways a belongs-to subject fails to name anything, kept apart
	// because the fixes differ: one caller sent half a subject and needs the
	// other half, and the other sent none and is asking a question this read
	// does not answer. See ErrPartialSubject and ErrUnattachedSubject.
	case errors.Is(err, ErrPartialSubject):
		return httperrors.ErrValidatingRequestInput, "a belongs-to subject needs both a type and an id", true
	case errors.Is(err, ErrUnattachedSubject):
		return httperrors.ErrValidatingRequestInput, "a belongs-to subject must name something", true

	// More ids than one statement may carry. It is bad input rather than a range
	// that ran out: the ceiling is a property of the dialect underneath and not
	// of where the caller has paged to, and the supported way to read more is
	// ListObjectsByIDsInBatches. The message says so without naming the limit,
	// which MaxObjectIDsPerRead already does for anybody writing the client.
	case errors.Is(err, ErrTooManyObjectIDs):
		return httperrors.ErrValidatingRequestInput, "too many object ids for one read", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrObjectNotFound):
		return codes.NotFound, true

	// AlreadyExists rather than FailedPrecondition, because the collision is on
	// a unique key and that is the code gRPC reserves for exactly that.
	case errors.Is(err, ErrObjectKeyTaken),
		errors.Is(err, ErrObjectKeyOccupied):
		return codes.AlreadyExists, true

	// InvalidArgument for the three shape refusals: each is an argument that is
	// bad regardless of the system's state, and none becomes valid by retrying.
	case errors.Is(err, ErrPartialSubject),
		errors.Is(err, ErrUnattachedSubject),
		errors.Is(err, ErrTooManyObjectIDs):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
