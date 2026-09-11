package sessions

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// Sentinels. HTTPMapper and GRPCMapper, in this package, map them onto status
// codes; errors/http and errors/grpc are primitives and know nothing of this
// package, which is why the mapping lives here and this package imports them
// rather than the other way around.
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

	// ErrPrincipalRequired indicates a call that had to name whose sessions it
	// was about and did not. It wraps errors.ErrEmptyInputParameter, so a
	// caller may check either.
	//
	// The empty principal is a session held by nobody — what Store.New
	// establishes — and it is refused here rather than matched, because a list
	// or a revocation over every anonymous session in a scope is not the
	// question anybody meant to ask.
	ErrPrincipalRequired = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty session principal")

	// ErrNoPrincipalIndex indicates a store whose backend cannot answer which
	// sessions a principal holds.
	//
	// It is a property of the backend rather than a misuse: sessions/cache is
	// a key-value store, which answers what is under a key and cannot answer
	// which keys belong to a person without a second structure beside it — and
	// a second structure recording which sessions are live is a second thing
	// that can disagree with the first about a revocation. sessions/database
	// has the index, because a table can carry the column.
	//
	// A deployment that needs "sign out my other devices" therefore needs the
	// database backend, and finds that out from this error rather than from a
	// list that comes back empty.
	ErrNoPrincipalIndex = platformerrors.New("session backend keeps no principal index")

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
