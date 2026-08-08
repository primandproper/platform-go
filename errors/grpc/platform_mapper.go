package grpc

import (
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v10/circuitbreaking"
	"github.com/primandproper/platform-go/v10/cryptography/requestsigning"
	"github.com/primandproper/platform-go/v10/database"
	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/idempotency"
	"github.com/primandproper/platform-go/v10/ratelimiting"

	"google.golang.org/grpc/codes"
)

// PlatformMapper maps platform-level errors to gRPC codes.
// It does not depend on any domain.
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
	case errors.Is(err, platformerrors.ErrNilInputParameter),
		errors.Is(err, platformerrors.ErrEmptyInputParameter),
		errors.Is(err, platformerrors.ErrNilInputProvided),
		errors.Is(err, platformerrors.ErrInvalidIDProvided),
		errors.Is(err, platformerrors.ErrEmptyInputProvided):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
