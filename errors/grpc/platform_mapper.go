package grpc

import (
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/dataprivacy"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/links"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/ratelimiting"
	textsearch "github.com/primandproper/platform-go/v13/search/text"
	vectorsearch "github.com/primandproper/platform-go/v13/search/vector"
	"github.com/primandproper/platform-go/v13/sessions"

	"google.golang.org/grpc/codes"
)

// PlatformMapper maps platform-level errors to gRPC codes.
// It does not depend on any domain.
//
// It covers the same sentinel set as the HTTP mapper, and deliberately so: a
// service exposing both transports would otherwise answer the same failure with
// a considered status on one and codes.Unknown on the other, and which one a
// client got would depend on how it happened to connect.
var PlatformMapper GRPCErrorMapper = platformMapper{}

type platformMapper struct{}

func (platformMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}
	switch {
	case errors.Is(err, database.ErrUserAlreadyExists):
		return codes.AlreadyExists, true
	case errors.Is(err, sql.ErrNoRows):
		return codes.NotFound, true
	case errors.Is(err, circuitbreaking.ErrCircuitBroken):
		return codes.Unavailable, true
	case errors.Is(err, platformerrors.ErrPermissionDenied):
		return codes.PermissionDenied, true
	// FailedPrecondition rather than Aborted: gRPC's own guidance gives
	// "rmdir on a non-empty directory" as the canonical FailedPrecondition
	// case, and a delete blocked by a live reference is the same shape. The
	// client must change the system state before retrying, not just retry.
	case errors.Is(err, platformerrors.ErrResourceInUse):
		return codes.FailedPrecondition, true
	// ResourceExhausted is gRPC's own name for "out of quota or rate", and its
	// documented client advice — back off and retry — is the right one here.
	// Unavailable would be wrong: nothing is down, and it invites the client to
	// fail over to another instance that shares the same limiter.
	case errors.Is(err, ratelimiting.ErrRateLimited):
		return codes.ResourceExhausted, true
	// The same code as a rate limit, and for the same reason: gRPC has one name
	// for "out of quota or rate", and a spent billing allowance is squarely
	// inside it. Checked before ErrNotEntitled, which it does not wrap but which
	// is the broader answer, so a caller that returns both does not collapse into
	// the vaguer one.
	case errors.Is(err, platformerrors.ErrQuotaExhausted):
		return codes.ResourceExhausted, true
	// PermissionDenied rather than FailedPrecondition, which was the other
	// candidate. gRPC's guidance is that PermissionDenied is for a caller that is
	// identified and may not do the thing — which is exactly true of an account
	// whose plan excludes a feature — while FailedPrecondition asks the client to
	// change system state, and paying for a subscription is not a state change
	// the RPC's client can perform. HTTP has 402 to be precise with; gRPC does
	// not, and inventing precision it lacks would only make the code unmappable
	// by a gateway.
	case errors.Is(err, platformerrors.ErrNotEntitled):
		return codes.PermissionDenied, true
	// Unauthenticated rather than PermissionDenied: gRPC's own guidance
	// separates "we do not know who you are" from "we know, and you may not".
	// A signature that does not verify is the first — nothing has been
	// identified yet, so there is nothing to deny.
	case errors.Is(err, requestsigning.ErrInvalidSignature),
		errors.Is(err, requestsigning.ErrStaleSignature):
		return codes.Unauthenticated, true
	// FailedPrecondition rather than NotFound, and the gap between the two is
	// the point. NotFound invites the client to try the same URL again; a link
	// that has been used, has expired, or has been revoked will never work
	// again, and the retry it invites is a person clicking a dead link twice.
	// FailedPrecondition's documented advice — change the state, then retry — is
	// exactly right: the state to change is "hold a live link", and the way to
	// change it is to ask for a new one.
	case errors.Is(err, links.ErrLinkAlreadyRedeemed),
		errors.Is(err, links.ErrLinkExpired),
		errors.Is(err, links.ErrLinkRevoked),
		errors.Is(err, links.ErrLinkNotFound):
		return codes.FailedPrecondition, true
	// A malformed token never named a link at all, which is ordinary bad input.
	case errors.Is(err, links.ErrInvalidToken):
		return codes.InvalidArgument, true
	// Unauthenticated for every unusable session — absent, forged, expired.
	// ErrNotFound alone covers all of them, since ErrExpired and the two timeout
	// errors wrap it, and nothing is lost by collapsing them: gRPC has one code
	// for "we do not know who you are", and telling a client apart "no such
	// session" from "that one expired" is an oracle for whether a guessed
	// identifier ever existed. The HTTP mapper splits the two only to vary the
	// message a person reads; the status is the same there too.
	case errors.Is(err, sessions.ErrNotFound):
		return codes.Unauthenticated, true
	// An operation nobody may read and an operation that does not exist are the
	// same answer, for the same reason the HTTP mapper gives.
	case errors.Is(err, operations.ErrOperationNotFound):
		return codes.NotFound, true
	// ResourceExhausted rather than Unavailable: nothing is down, the fleet is
	// simply at its subscription ceiling, and the client should back off and
	// retry rather than fail over to an instance with the same ceiling.
	case errors.Is(err, operations.ErrTooManyWatchers):
		return codes.ResourceExhausted, true
	case errors.Is(err, dataprivacy.ErrRequestNotFound):
		return codes.NotFound, true
	// FailedPrecondition for both: the request exists and the caller may see it,
	// but it is not in the state the call needs. The state has to change —
	// somebody confirms the request, or the export finishes — before a retry can
	// succeed, which is exactly what FailedPrecondition tells a client.
	case errors.Is(err, dataprivacy.ErrNotAwaitingConfirmation),
		errors.Is(err, dataprivacy.ErrArtifactUnavailable):
		return codes.FailedPrecondition, true
	case errors.Is(err, dataprivacy.ErrEmptySubjectID),
		errors.Is(err, dataprivacy.ErrUnknownRequestType):
		return codes.InvalidArgument, true
	// Aborted is gRPC's concurrency-conflict code, and its documented advice —
	// retry at a higher level — is exactly right here: the work may still
	// succeed, and the client should ask again with the same key.
	case errors.Is(err, idempotency.ErrInFlight):
		return codes.Aborted, true
	case errors.Is(err, idempotency.ErrFingerprintMismatch),
		errors.Is(err, idempotency.ErrKeyRequired),
		errors.Is(err, idempotency.ErrKeyTooLong),
		errors.Is(err, idempotency.ErrKeyInvalid):
		return codes.InvalidArgument, true
	// OutOfRange rather than InvalidArgument, which is the distinction gRPC's own
	// guidance draws between the two: InvalidArgument is an argument bad
	// regardless of the system's state, OutOfRange is one that ran past the end
	// of a range. A cursor at page 400 is well-formed and was well-formed when
	// the index issued it; it is only past the end. The alternatives are worse in
	// both directions — Internal turns a client-correctable refusal into a page
	// alert, and OK with an empty page tells the client it has seen everything.
	case errors.Is(err, textsearch.ErrResultWindowExceeded):
		return codes.OutOfRange, true
	// InvalidArgument for a cursor the index did not issue, which is the other
	// half of that distinction: nothing about the system's state makes it valid,
	// so it is bad input rather than exhausted range. Usually it is a cursor
	// carried over from a database-backed page — the two kinds share a field —
	// or one left over from a backend swap.
	case errors.Is(err, textsearch.ErrInvalidCursor),
		errors.Is(err, textsearch.ErrEmptyQueryProvided):
		return codes.InvalidArgument, true
	// The vector index's request-shaped refusals: a missing vector is a NotFound
	// like any other, and the other two are queries the index cannot evaluate.
	// Its construction-time sentinels are deliberately unmapped, for the reason
	// the HTTP mapper spells out where it does the same.
	case errors.Is(err, vectorsearch.ErrNotFound):
		return codes.NotFound, true
	case errors.Is(err, vectorsearch.ErrEmptyEmbedding),
		errors.Is(err, vectorsearch.ErrDimensionMismatch):
		return codes.InvalidArgument, true
	case errors.Is(err, platformerrors.ErrNilInputParameter),
		errors.Is(err, platformerrors.ErrEmptyInputParameter),
		errors.Is(err, platformerrors.ErrInvalidIDProvided),
		errors.Is(err, platformerrors.ErrEmptyInputProvided),
		errors.Is(err, platformerrors.ErrUnrecognizedInputValue):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
