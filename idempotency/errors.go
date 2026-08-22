package idempotency

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Sentinels. errors/http and errors/grpc map these onto status codes, so those
// packages import this one. That direction is load-bearing: nothing here may
// import errors/http or errors/grpc, or the cycle closes. It is also why the
// transport adapters live in their own packages rather than here.
var (
	// ErrInFlight indicates the key names work that is currently running
	// elsewhere. The caller has no way to know whether it will succeed, so the
	// only safe answer is to refuse and let the client retry later.
	ErrInFlight = platformerrors.New("idempotency key is in flight")
	// ErrFingerprintMismatch indicates the key was already used for a
	// different request. Replaying the stored result would hide a client bug,
	// so the reuse is reported instead.
	ErrFingerprintMismatch = platformerrors.New("idempotency key reused with a different request")
	// ErrKeyRequired indicates an empty key was supplied.
	ErrKeyRequired = platformerrors.New("empty idempotency key")
	// ErrKeyTooLong indicates a key longer than the configured maximum.
	ErrKeyTooLong = platformerrors.New("idempotency key exceeds the maximum length")
	// ErrKeyInvalid indicates a key containing bytes outside printable ASCII.
	ErrKeyInvalid = platformerrors.New("idempotency key contains disallowed characters")
	// ErrStoreUnavailable indicates the record store could not be reached and
	// the manager is configured to fail closed. Running the work anyway could
	// repeat an effect that already happened.
	ErrStoreUnavailable = platformerrors.New("idempotency store unavailable")
	// ErrEmptyFingerprint indicates Do was called without a fingerprint. An
	// empty one would make every request for a key look identical and disable
	// mismatch detection entirely, so it is rejected rather than defaulted.
	ErrEmptyFingerprint = platformerrors.New("empty idempotency fingerprint")
	// ErrNilStore indicates NewManager was called without a record store. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil idempotency store")
	// ErrNilLocker indicates NewManager was called without a locker. It has no
	// default: an implicit noop would silently remove mutual exclusion. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilLocker = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil idempotency locker")
	// ErrNilFunc indicates Do was called with no work to run. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilFunc = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil idempotency func")
	// ErrInvalidTTL indicates a non-positive TTL was configured.
	ErrInvalidTTL = platformerrors.New("invalid idempotency TTL")
	// ErrRecordableTypeMismatch indicates WithRecordable was given a predicate
	// for a type other than the Manager's. Option carries no type parameter, so
	// the compiler cannot catch this; NewManager reports it instead.
	ErrRecordableTypeMismatch = platformerrors.New("recordable predicate type does not match manager type")
)
