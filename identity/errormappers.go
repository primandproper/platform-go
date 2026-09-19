package identity

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels, and the reason they are
// here rather than in errors/http and errors/grpc.
//
// Those two packages are primitives. They may know about database,
// ratelimiting and the platform sentinels, which are primitives too, and
// nothing above them — so the switch that decides what a taken username means
// on the wire lives beside the error that says it was taken.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a
// side effect a consumer cannot opt out of. The composition root registers the
// domain tier, and for this module that is one call — errormappers.Register.
//
// The sentinels absent from both switches are absent on purpose. The nil-argument
// ones wrap errors.ErrNilInputParameter and the three malformed-input ones wrap
// errors.ErrUnrecognizedInputValue, so the platform mappers already answer them
// and a case here would be a second copy that could disagree.
// internal/sentinelmatrix records which sentinel is in which of the three
// states, and fails when one is in none.
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

// ClientSafeSentinels are the sentinels whose own text a gRPC server may return
// to a caller verbatim, handed to errors/grpc.RegisterClientSafeSentinels by
// errormappers.Register alongside the five mappers.
//
// They are the twelve the mappers claim, and the list is the same twelve on
// purpose:
// each was given a status because a client acts on it, and a client acting on
// it needs to know which one it got. gRPC derives its message from the code,
// and the codes collide — the two collisions are both AlreadyExists, and an
// expired invitation, a last owner and a missing default account are all
// FailedPrecondition — so without this a client in a language with no access
// to the encoded details is told the code's name three times for three
// different remedies. None of the twelve names a table, a key or a policy: what
// each says is the whole of what the caller needs and the whole of what this
// package knows.
//
// ErrSignInNotAdmitted is on it for the same reason and one of its own: its code
// is PermissionDenied, which is also what a consumer's authorization layer
// answers with, so a client reading the code alone cannot tell "your account is
// suspended" from "you may not do that". Its own words say which, and they name
// no status — the explanation an operator wrote for a banned user goes out at the
// sign-in door, through authentication/signin, and not here.
//
// The nil-argument and malformed-input sentinels are not here. They wrap
// platform sentinels that are already on errors/grpc's own list, so the
// platform's words reach the client for those without a second registration.
var ClientSafeSentinels = []error{
	ErrUserNotFound,
	ErrAccountNotFound,
	ErrMembershipNotFound,
	ErrInvitationNotFound,
	ErrUsernameTaken,
	ErrEmailAddressTaken,
	ErrLastAccountOwner,
	ErrNoDefaultAccount,
	ErrInvitationExpired,
	ErrScopeMismatch,
	ErrDisplayNameTooLong,
	ErrSignInNotAdmitted,
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
	// Ordered before ErrInvitationNotFound, which it does not wrap but which a
	// reader will expect to shadow it. An expired invitation is told apart from
	// an absent one on purpose: the recipient followed a real link, and "ask for
	// another" is a different instruction from "that link was never valid". It
	// is not an oracle, because holding the token is already proof the
	// invitation existed.
	case errors.Is(err, ErrInvitationExpired):
		return httperrors.ErrDataNotFound, "invitation has expired", true

	// The four absences share a code and a message that names what was not
	// found. A user in another directory reads as absent, which is what it is
	// from here, and is the answer that does not turn a read into an oracle for
	// which usernames exist in somebody else's tenant.
	case errors.Is(err, ErrUserNotFound):
		return httperrors.ErrDataNotFound, "user not found", true
	case errors.Is(err, ErrAccountNotFound):
		return httperrors.ErrDataNotFound, "account not found", true
	case errors.Is(err, ErrMembershipNotFound):
		return httperrors.ErrDataNotFound, "membership not found", true
	case errors.Is(err, ErrInvitationNotFound):
		return httperrors.ErrDataNotFound, "invitation not found", true

	// The two collisions. They are the reason registration needs a considered
	// status at all: "your input collides" and "the database is unwell" decide
	// whether a client retries with the same value or a different one, and a
	// 500 tells them to do neither.
	case errors.Is(err, ErrUsernameTaken):
		return httperrors.ErrResourceConflict, "username is already registered", true
	case errors.Is(err, ErrEmailAddressTaken):
		return httperrors.ErrResourceConflict, "email address is already registered", true

	// The two states an act is refused from rather than forbidden outright. Both
	// are fixable by the caller, in a specific order, and the message says which
	// act comes first.
	case errors.Is(err, ErrLastAccountOwner):
		return httperrors.ErrResourceConflict, "account must be transferred to another owner first", true
	case errors.Is(err, ErrNoDefaultAccount):
		return httperrors.ErrResourceConflict, "user must be given a default account first", true

	// The one refusal on authority this package makes. A user whose status does
	// not admit sign-in is somebody the directory knows and will not answer for,
	// which is a 403 and not a 404: the caller is signed in as far as their token
	// is concerned, and an absence code would send a client to a "no such page"
	// screen for an account that was suspended. The message collapses the three
	// statuses, as the sentinel does, because the door already told them which —
	// see ErrSignInNotAdmitted.
	case errors.Is(err, ErrSignInNotAdmitted):
		return httperrors.ErrUserIsBanned, "account is not permitted to sign in", true

	// A write whose entity names a different tenant than the call did. It is the
	// caller's mistake and it is a request-shaped one, so it reads as a bad
	// request rather than as a permission failure — nothing was refused on
	// authority, the two halves of the request disagreed.
	case errors.Is(err, ErrScopeMismatch):
		return httperrors.ErrValidatingRequestInput, "entity does not belong to the named tenant", true

	// The one rule a display name has. It is bad input rather than a conflict:
	// nothing was taken and nothing was refused on authority, the value is
	// simply longer than the column that holds it. The sentinel's own wrapping
	// carries the limit and the length for whoever reads a log; what a client
	// needs here is which field to shorten.
	case errors.Is(err, ErrDisplayNameTooLong):
		return httperrors.ErrValidatingRequestInput, "display name is too long", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	// FailedPrecondition rather than NotFound, and the one place the two mappers
	// choose differently. gRPC has a code for "the thing exists and the state is
	// wrong", HTTP does not, and an expired invitation is exactly that: the row
	// is there and the answer is no longer available. The HTTP side collapses it
	// into the absence codes and varies the message instead.
	case errors.Is(err, ErrInvitationExpired):
		return codes.FailedPrecondition, true

	case errors.Is(err, ErrUserNotFound),
		errors.Is(err, ErrAccountNotFound),
		errors.Is(err, ErrMembershipNotFound),
		errors.Is(err, ErrInvitationNotFound):
		return codes.NotFound, true

	case errors.Is(err, ErrUsernameTaken),
		errors.Is(err, ErrEmailAddressTaken):
		return codes.AlreadyExists, true

	case errors.Is(err, ErrLastAccountOwner),
		errors.Is(err, ErrNoDefaultAccount):
		return codes.FailedPrecondition, true

	// Known, and refused anyway, which is what PermissionDenied means. It is not
	// Unauthenticated: the caller proved who they are and the directory will not
	// answer for them, and a client told to authenticate again would loop on a
	// credential that keeps working.
	case errors.Is(err, ErrSignInNotAdmitted):
		return codes.PermissionDenied, true

	case errors.Is(err, ErrScopeMismatch),
		errors.Is(err, ErrDisplayNameTooLong):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
