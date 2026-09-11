package comments

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
// switch that decides what a refused reply means on the wire lives beside the
// error that refused it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a
// side effect a consumer cannot opt out of. The composition root registers the
// domain tier, and for this module that is one call — errormappers.Register.
//
// They map more than comments/grpc raises, deliberately. A consumer serving
// their own handler over comments.Store meets ErrScopeMismatch and
// ErrEmptyAuthor, neither of which this module's own surface can produce — it
// takes both off the connection — and a mapping that only covered the eight
// RPCs would make the answer depend on which transport happened to ask.
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

// ClientSafeSentinels are the sentinels whose own text a gRPC server may return
// to a caller verbatim, handed to errors/grpc.RegisterClientSafeSentinels by
// errormappers.Register alongside the mappers.
//
// This package's refusals are unusually well suited to it, because every one of
// them is something a person did and can undo. Four of the six share
// codes.InvalidArgument and two share codes.NotFound, so the code alone tells a
// client nothing about which field to put a red border around — and the
// sentence each one already carries is the sentence somebody needs.
//
// ErrNestedReply is the clearest case: "a comment reply may not itself be
// replied to" is a rule the person typing did not know about, and it is not
// recoverable from "InvalidArgument" by anybody.
//
// None of the six names a person, a tenant, or a row somebody else owns.
// ErrCommentNotFound is deliberately absent for the opposite reason: its text is
// fine, but it is the answer given for a comment in another tenant's scope as
// well as for one that is not there, and the two must stay indistinguishable.
var ClientSafeSentinels = []error{
	ErrUnknownTargetType,
	ErrTargetNotFound,
	ErrParentNotFound,
	ErrNestedReply,
	ErrTargetMismatch,
	ErrEmptyBody,
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
	// The three rows a caller can name and not reach. A comment absent,
	// archived, or in another tenant's scope is one answer, which is what keeps a
	// read from being an enumeration oracle over other tenants' discussions.
	//
	// The parent is separate from the comment because the missing row is not the
	// one the caller named: they named a reply, and what is missing is what they
	// are replying to. The target is separate again, and its message says the
	// thing being discussed rather than the discussion — a client shown it has a
	// stale list rather than a bug.
	case errors.Is(err, ErrCommentNotFound):
		return httperrors.ErrDataNotFound, "no such comment", true
	case errors.Is(err, ErrParentNotFound):
		return httperrors.ErrDataNotFound, "the comment being replied to is no longer there", true
	case errors.Is(err, ErrTargetNotFound):
		return httperrors.ErrDataNotFound, "the thing being commented on is no longer there", true

	// The refusals a caller corrects by sending something else, each naming the
	// field rather than the rule.
	case errors.Is(err, ErrUnknownTargetType):
		return httperrors.ErrValidatingRequestInput, "comments are not accepted on that kind of thing", true
	case errors.Is(err, ErrNestedReply):
		return httperrors.ErrValidatingRequestInput, "a comment reply may not itself be replied to", true
	case errors.Is(err, ErrTargetMismatch):
		return httperrors.ErrValidatingRequestInput, "a reply belongs to the same discussion as the comment it replies to", true
	case errors.Is(err, ErrEmptyBody):
		return httperrors.ErrValidatingRequestInput, "a comment needs something in it", true
	case errors.Is(err, ErrEmptyParent):
		return httperrors.ErrValidatingRequestInput, "naming which comment's replies are wanted is required", true
	case errors.Is(err, ErrEmptyTargetType), errors.Is(err, ErrEmptyTargetID):
		return httperrors.ErrValidatingRequestInput, "a comment must name both what kind of thing it is about and which one", true
	case errors.Is(err, ErrEmptyAuthor):
		return httperrors.ErrValidatingRequestInput, "a comment must name who wrote it", true
	case errors.Is(err, ErrScopeMismatch):
		return httperrors.ErrValidatingRequestInput, "the comment does not belong to that scope", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrCommentNotFound), errors.Is(err, ErrParentNotFound),
		errors.Is(err, ErrTargetNotFound):
		return codes.NotFound, true

	// InvalidArgument rather than FailedPrecondition for all seven, including
	// the nested reply. FailedPrecondition is gRPC's code for a well-formed
	// request the system's state refuses, which a client fixes by changing that
	// state and retrying; none of these is that. A reply to a reply is fixed by
	// naming the root instead, an unknown target type by naming one the catalog
	// holds — the remedy in every case is a different request.
	case errors.Is(err, ErrUnknownTargetType), errors.Is(err, ErrNestedReply),
		errors.Is(err, ErrTargetMismatch), errors.Is(err, ErrEmptyBody),
		errors.Is(err, ErrEmptyParent), errors.Is(err, ErrEmptyTargetType),
		errors.Is(err, ErrEmptyTargetID), errors.Is(err, ErrEmptyAuthor),
		errors.Is(err, ErrScopeMismatch):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
