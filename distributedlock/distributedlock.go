package distributedlock

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v3/errors"
)

var (
	// ErrLockNotAcquired indicates Acquire could not obtain the lock immediately
	// because another caller currently holds it. Callers that want to wait should
	// compose Acquire with a retry/backoff loop themselves — the Locker interface
	// does not retry internally.
	ErrLockNotAcquired = platformerrors.New("lock not acquired")
	// ErrLockNotHeld indicates Release or Refresh was called on a lock the caller
	// no longer owns. Reasons include TTL expiration, the lock being stolen by
	// another caller after expiration, double-release, or — for the postgres
	// provider — the underlying connection having been closed out from under us.
	ErrLockNotHeld = platformerrors.New("lock not held")
	// ErrNilConfig indicates a nil provider config was passed to a constructor.
	ErrNilConfig = platformerrors.New("nil distributedlock config")
	// ErrInvalidTTL indicates a non-positive TTL was supplied to Acquire or Refresh.
	ErrInvalidTTL = platformerrors.New("invalid lock TTL")
	// ErrEmptyKey indicates an empty key was supplied to Acquire.
	ErrEmptyKey = platformerrors.New("empty lock key")
	// ErrNilDatabaseClient indicates a nil database.Client was passed to a postgres-
	// backed provider.
	ErrNilDatabaseClient = platformerrors.New("nil database client")
)

type (
	// Locker is the manager atom. It hands out Lock handles keyed by string. Locker
	// implementations must be safe for concurrent use; the Lock handles they return
	// are owned by the goroutine that called Acquire and are NOT goroutine-safe.
	Locker interface {
		// Acquire attempts to acquire the lock named `key` with the supplied TTL.
		// Returns ErrLockNotAcquired immediately if the lock is currently held by
		// another caller. There is no internal retry — callers wrap with
		// retry/backoff themselves.
		Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, error)
		// Ping verifies the underlying backend is reachable.
		Ping(ctx context.Context) error
		// Close releases any backend resources held by the Locker. Outstanding Lock
		// handles obtained from this Locker may become invalid after Close.
		Close() error
	}

	// Lock is the handle returned from Acquire. It carries the ownership token
	// internally and is the only way to release or refresh the lock. Lock handles
	// are owned by a single goroutine — they must not be shared.
	Lock interface {
		// Key returns the lock name this handle owns.
		Key() string
		// TTL returns the configured expiration for this lock at the time it was
		// last acquired or refreshed. It is not adjusted as time passes.
		TTL() time.Duration
		// Release releases the lock. Returns ErrLockNotHeld if the caller no longer
		// owns the lock (expiration, theft after expiration, double-release).
		Release(ctx context.Context) error
		// Refresh extends the lock's TTL to the supplied value. Returns
		// ErrLockNotHeld if the caller no longer owns the lock.
		Refresh(ctx context.Context, ttl time.Duration) error
	}
)
