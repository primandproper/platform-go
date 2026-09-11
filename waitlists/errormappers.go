package waitlists

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
// switch that decides what a refused signup means on the wire lives beside the
// error that refused it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a side
// effect a consumer cannot opt out of. The composition root registers the domain
// tier, and for this module that is one call — errormappers.Register.
//
// The sentinels absent from both switches are absent on purpose. Every one of
// them wraps a platform sentinel the platform mappers already answer — four nil
// arguments and five empty ones — and a case here would be a second copy of a
// decision already made, free to drift from it.
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

// ClientSafeSentinels are the sentinels whose own text a gRPC server may return
// to a caller verbatim, handed to errors/grpc.RegisterClientSafeSentinels by
// errormappers.Register alongside the mappers.
//
// They are the five refusals a person filling in a signup form or clicking an
// unsubscribe link meets, and they are here because the codes collide: four of
// them are FailedPrecondition and every one of the five has a different remedy.
// A person told "FailedPrecondition" learns nothing, and the page rendering it
// has to choose between "we have stopped taking signups", "you are already on
// this list", "you asked us to leave you alone" and "somebody already sent that
// invitation" from a single code.
//
// Each is written for the person reading it and none names anybody else. The
// disclosure they do make is bounded and is the point of the refusal: a caller
// who already holds an address is told that this address is on this list, which
// is what they submitted the form to find out. The read that would answer the
// same question for an address the caller does not hold —
// SignupStore.GetSignupByContact — is not on the public half of
// waitlists/grpc, precisely so that this is not an oracle.
var ClientSafeSentinels = []error{
	ErrListClosed,
	ErrAlreadySignedUp,
	ErrContactWithdrawn,
	ErrWrongStatus,
	ErrAlreadyWithdrawn,
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
	// The two absences. Archived and in another tenant's scope read the same
	// way, which is what keeps a keyed read from being an enumeration oracle
	// over other tenants' rows.
	case errors.Is(err, ErrListNotFound):
		return httperrors.ErrDataNotFound, "no such waitlist", true
	case errors.Is(err, ErrSignupNotFound):
		return httperrors.ErrDataNotFound, "no such waitlist signup", true

	// The five refusals, each quoted rather than paraphrased. They are written
	// for the person reading them, which is why they are also the
	// ClientSafeSentinels above — a paraphrase here and the sentinel's own
	// wording on the gRPC side would be one refusal with two texts.
	//
	// ErrListClosed is deliberately not an absence. A closed list is a page that
	// says "we are no longer taking signups" and a missing one is a broken link,
	// and collapsing the two sends somebody to the wrong page.
	case errors.Is(err, ErrListClosed):
		return httperrors.ErrResourceConflict, ErrListClosed.Error(), true
	case errors.Is(err, ErrAlreadySignedUp):
		return httperrors.ErrResourceConflict, ErrAlreadySignedUp.Error(), true
	case errors.Is(err, ErrContactWithdrawn):
		return httperrors.ErrResourceConflict, ErrContactWithdrawn.Error(), true
	case errors.Is(err, ErrWrongStatus):
		return httperrors.ErrResourceConflict, ErrWrongStatus.Error(), true
	case errors.Is(err, ErrAlreadyWithdrawn):
		return httperrors.ErrResourceConflict, ErrAlreadyWithdrawn.Error(), true

	// A write whose entity names a different tenant than the call did. The two
	// halves of the request disagreed, which is a bad request rather than a
	// refusal on authority.
	case errors.Is(err, ErrScopeMismatch):
		return httperrors.ErrValidatingRequestInput, "the waitlist row does not belong to that scope", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrListNotFound), errors.Is(err, ErrSignupNotFound):
		return codes.NotFound, true

	// AlreadyExists rather than FailedPrecondition, because the collision is on
	// a unique key and that is the code gRPC reserves for exactly that. It is
	// the one of the five that names a row rather than a state.
	case errors.Is(err, ErrAlreadySignedUp):
		return codes.AlreadyExists, true

	// FailedPrecondition rather than Aborted for all four: the request is well
	// formed and correctly addressed, and the row is not in a state that admits
	// it. Aborted invites a client library to retry, and none of these four gets
	// better on a second attempt — a closed list stays closed, a withdrawal is
	// permanent, and a transition that lost its race lost it for good.
	case errors.Is(err, ErrListClosed), errors.Is(err, ErrContactWithdrawn),
		errors.Is(err, ErrWrongStatus), errors.Is(err, ErrAlreadyWithdrawn):
		return codes.FailedPrecondition, true

	case errors.Is(err, ErrScopeMismatch):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
