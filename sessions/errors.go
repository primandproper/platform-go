package sessions

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Sentinels. errors/http maps these onto status codes, so that package imports
// this one. That direction is load-bearing: nothing here may import
// errors/http or errors/grpc, or the cycle closes.
//
// The four absence errors form a chain — ErrIdleTimeout and
// ErrAbsoluteTimeout wrap ErrExpired, which wraps ErrNotFound — so a caller
// picks the resolution it cares about. A middleware deciding whether to
// redirect to a login page checks ErrNotFound; a page that wants to say "you
// were signed out for inactivity" checks ErrIdleTimeout.
var (
	// ErrNotFound indicates no session is stored under the identifier. It is
	// also what a record written by another shape of this package reads as,
	// deliberately: a stale record is a re-login, and misreading one would hand
	// a user a payload decoded from bytes that meant something else.
	ErrNotFound = platformerrors.New("session not found")
	// ErrExpired indicates a session was found but is past one of its
	// deadlines. It wraps ErrNotFound, so a caller that does not care why the
	// session is unusable checks only that.
	ErrExpired = platformerrors.Wrap(ErrNotFound, "session expired")
	// ErrIdleTimeout indicates the session went unread for longer than the
	// idle timeout. It wraps ErrExpired.
	ErrIdleTimeout = platformerrors.Wrap(ErrExpired, "session idle timeout elapsed")
	// ErrAbsoluteTimeout indicates the session outlived its absolute timeout,
	// which no amount of activity extends. It wraps ErrExpired.
	ErrAbsoluteTimeout = platformerrors.Wrap(ErrExpired, "session absolute timeout elapsed")

	// ErrIDConflict indicates Create was given an identifier that already
	// exists. Identifiers are 256 bits of cryptographic randomness, so this
	// means a backend was handed an identifier it did not mint, not that two
	// sessions collided.
	ErrIDConflict = platformerrors.New("session identifier already in use")

	// ErrIDRequired indicates an empty identifier was supplied. It wraps
	// errors.ErrEmptyInputParameter, so a caller may check either.
	ErrIDRequired = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty session identifier")

	// ErrNilBackend indicates NewStore was called without a backend. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilBackend = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil session backend")

	// ErrNoTimeout indicates a Policy with neither an absolute nor an idle
	// timeout. Such a store never releases a session, so it is rejected at
	// construction rather than discovered as unbounded growth.
	ErrNoTimeout = platformerrors.New("session policy sets no timeout")

	// ErrTouchExceedsIdleTimeout indicates a touch interval at least as long as
	// the idle timeout, which would let a session idle out between the reads
	// that were supposed to keep it alive.
	ErrTouchExceedsIdleTimeout = platformerrors.New("session touch interval is not shorter than the idle timeout")

	// ErrNegativeTouchInterval indicates a negative touch interval. Zero is
	// meaningful — refresh on every read — so it cannot stand in for "unset".
	ErrNegativeTouchInterval = platformerrors.New("negative session touch interval")
)
