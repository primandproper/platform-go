package http

import (
	"database/sql"

	"github.com/primandproper/platform-go/v10/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/idempotency"
	"github.com/primandproper/platform-go/v10/ratelimiting"
)

// codeToError inverts PlatformMapper for the codes that came from exactly one
// platform sentinel.
//
// Most codes are not in here, and cannot be. ErrValidatingRequestInput is
// produced by five different sentinels plus every domain that registers a
// mapper; there is no single error to hand back for it, and picking one would
// be a guess a client would then branch on. The entries below are the ones
// where the forward mapping is a bijection, so the round trip is lossless.
var codeToError = map[ErrorCode]error{
	ErrCircuitBroken:          circuitbreaking.ErrCircuitBroken,
	ErrDataNotFound:           sql.ErrNoRows,
	ErrIdempotencyKeyInFlight: idempotency.ErrInFlight,
	ErrIdempotencyKeyReused:   idempotency.ErrFingerprintMismatch,
	ErrNotEntitled:            platformerrors.ErrNotEntitled,
	ErrQuotaExhausted:         platformerrors.ErrQuotaExhausted,
	ErrResourceConflict:       platformerrors.ErrResourceInUse,
	ErrTooManyRequests:        ratelimiting.ErrRateLimited,
	ErrUserIsNotAuthorized:    platformerrors.ErrPermissionDenied,
}

// ErrorForCode returns the platform sentinel an ErrorCode was mapped from, or
// nil when the code has no single sentinel behind it.
//
// This is the direction a client reads. A service answers 429 with code E116;
// its typed client parses the envelope and, rather than handing its caller an
// HTTP status to switch on, hands back ratelimiting.ErrRateLimited — the same
// sentinel a caller inside the serving process would have gotten, matched the
// same way with errors.Is. That is what makes a remote call substitutable for a
// local one at the error boundary.
//
// The returned error is the sentinel itself, unwrapped and unannotated. Callers
// that want the remote context on it should wrap it with their own.
//
// A nil result means only that this package cannot name the error; it does not
// mean the response was a success. Fall back to the status code.
func ErrorForCode(code ErrorCode) error {
	return codeToError[code]
}
