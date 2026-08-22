// Package noop is the distributedlock implementation for a deployment with
// nothing to coordinate with: Acquire always succeeds immediately, WithLock
// always runs fn, and TryWithLock always reports the lock as taken.
//
// # This provider is correct at one replica
//
// It provides no mutual exclusion of any kind, which makes it correct at
// exactly one process and silently wrong above that. Two replicas holding "the
// same" lock both proceed into the critical section, and nothing here detects
// the second one or reports the contention it did not arbitrate. It fails as a
// double-processed job or a lost update rather than as an error. That is a
// constraint on the deployment, not a tuning knob — a service that scales out
// wants the redis or postgres provider instead, where the lock lives somewhere
// both replicas can see it.
//
// Its lock handles are real enough to pass around: Key and TTL echo back what
// Acquire was given, and Refresh updates the TTL it will echo. Nothing is
// stored anywhere and no expiry is ever enforced, so a handle that outlives its
// TTL is still held.
//
// distributedlock/config builds either type for the "noop" provider name.
package noop

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/distributedlock"
)

var (
	_ distributedlock.Locker = (*Locker)(nil)
	_ distributedlock.Lock   = (*lock)(nil)
)

// Locker is a no-op distributedlock.Locker. Acquire always succeeds, Release and
// Refresh are no-ops, Ping returns nil. Use this when distributed locking is not
// needed in a given deployment (single replica, dev environments, etc.).
type Locker struct{}

// NewLocker returns a no-op Locker.
func NewLocker() *Locker {
	return &Locker{}
}

// Acquire always returns a trivial lock handle.
func (*Locker) Acquire(_ context.Context, key string, ttl time.Duration) (distributedlock.Lock, error) {
	return &lock{key: key, ttl: ttl}, nil
}

// Ping is a no-op that always succeeds.
func (*Locker) Ping(_ context.Context) error {
	return nil
}

// Close is a no-op.
func (*Locker) Close() error {
	return nil
}

// lock is a trivial Lock implementation paired with the noop Locker.
type lock struct {
	key string
	ttl time.Duration
}

// Key returns the lock key.
func (l *lock) Key() string {
	return l.key
}

// TTL returns the configured TTL.
func (l *lock) TTL() time.Duration {
	return l.ttl
}

// Release is a no-op.
func (*lock) Release(_ context.Context) error {
	return nil
}

// Refresh updates the configured TTL but does no work.
func (l *lock) Refresh(_ context.Context, ttl time.Duration) error {
	l.ttl = ttl
	return nil
}

var _ distributedlock.ScopedLocker = (*ScopedLocker)(nil)

// ScopedLocker is a no-op distributedlock.ScopedLocker: fn always runs, as if
// the lock were acquired immediately. Use it where scoped locking is wired but
// a deployment has nothing to coordinate with (single replica, dev).
type ScopedLocker struct{}

// NewScopedLocker returns a no-op ScopedLocker.
func NewScopedLocker() *ScopedLocker {
	return &ScopedLocker{}
}

// WithLock runs fn immediately.
func (*ScopedLocker) WithLock(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// TryWithLock runs fn immediately, always reporting the lock as acquired.
func (*ScopedLocker) TryWithLock(ctx context.Context, _ string, fn func(ctx context.Context) error) (bool, error) {
	return true, fn(ctx)
}
