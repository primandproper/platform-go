package cache

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/sessions"
)

// serviceName names the loggers and spans this backend emits. The counters live
// on the Store, which is the layer that knows what an operation meant.
const serviceName = "sessions_cache"

// Backend stores session records in a cache.Cache. It is exported, and returned
// by NewBackend, so a caller who has chosen cache-backed sessions can depend on
// that choice rather than on the sessions.Backend seam — matching
// sessions/database, whose NewBackend has always returned its own *Backend.
type Backend[T any] struct {
	cache cache.Cache[sessions.Record[T]]
	o11y  observability.Observer
}

var _ sessions.Backend[struct{}] = (*Backend[struct{}])(nil)

// NewBackend builds a sessions.Backend over a cache.
//
// The cache is required and has no default, because which one it is decides
// what the sessions mean. A memory cache is per-process: two replicas do not
// see each other's sessions, so a user is signed in to whichever one their
// request lands on. Redis is the production answer; memory is for tests and for
// single-process services that accept losing every session on restart.
//
// The cache's own default expiry is never used — every write carries the
// deadline the Store computed from its Policy — so a cache built solely for
// sessions can be configured with any expiry at all.
func NewBackend[T any](c cache.Cache[sessions.Record[T]], opts ...Option) (*Backend[T], error) {
	if c == nil {
		return nil, ErrNilCache
	}

	o := newOptions(opts)

	return &Backend[T]{
		cache: c,
		o11y:  observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}, nil
}

// Load reads the record stored under id.
func (b *Backend[T]) Load(ctx context.Context, id string) (*sessions.Record[T], error) {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	record, err := b.cache.Get(ctx, id)
	if err != nil {
		return nil, translate(err, "reading session record")
	}

	if record == nil {
		return nil, sessions.ErrNotFound
	}

	return record, nil
}

// Create stores a record under a freshly minted identifier.
//
// It writes without checking first, which is the one place this backend takes
// an identifier's uniqueness on faith rather than enforcing it. The identifier
// is 256 bits from crypto/rand; a read-before-write here would cost a round
// trip on the hot path to defend against a collision that will not happen.
// ErrIDConflict is therefore part of the Backend contract that only the
// database backend can actually report.
func (b *Backend[T]) Create(ctx context.Context, id string, record *sessions.Record[T], ttl time.Duration) error {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	if err := b.cache.Set(ctx, id, record, cache.WithExpiry(ttl)); err != nil {
		return translate(err, "storing new session record")
	}

	return nil
}

// Update overwrites the record stored under an existing identifier.
//
// The condition is what stops a signed-out session from coming back. A request
// that loaded a session just before Delete removed it would otherwise write it
// back afterwards, complete with a fresh idle deadline, and the sign-out would
// not have happened.
//
// SetIfPresent makes that one operation, so there is no interval for the delete
// to land in: the write either precedes it or is refused by it. An absent record
// comes back as ErrNotFound, which translate turns into sessions.ErrNotFound —
// the same answer a load would give, because a session that was revoked
// mid-request is exactly a session that is not there.
func (b *Backend[T]) Update(ctx context.Context, id string, record *sessions.Record[T], ttl time.Duration) error {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	if err := b.cache.SetIfPresent(ctx, id, record, cache.WithExpiry(ttl)); err != nil {
		return translate(err, "updating session record")
	}

	return nil
}

// Rename moves a record from oldID to newID.
//
// The new identifier is written before the old one is removed, deliberately. In
// the reverse order a failed write leaves the user with no session at all,
// signed out mid-privilege-change; in this order a failed delete leaves the old
// identifier valid, and the error says so, so the caller refuses the privilege
// change rather than proceeding with a session an attacker may hold. Neither
// order is atomic here — that is what sessions/database's transaction is for.
func (b *Backend[T]) Rename(
	ctx context.Context,
	oldID, newID string,
	record *sessions.Record[T],
	ttl time.Duration,
) error {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	if _, err := b.cache.Get(ctx, oldID); err != nil {
		return translate(err, "checking session record before renewal")
	}

	if err := b.cache.Set(ctx, newID, record, cache.WithExpiry(ttl)); err != nil {
		return translate(err, "storing renewed session record")
	}

	if err := b.cache.Delete(ctx, oldID); err != nil {
		// The renewed record is left in place. Removing it would leave the
		// caller with no session at all on top of an old identifier that still
		// works, and the caller is about to refuse the privilege change anyway.
		return translate(err, "removing renewed session's previous record")
	}

	return nil
}

// Delete removes the record stored under id.
func (b *Backend[T]) Delete(ctx context.Context, id string) error {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	if err := b.cache.Delete(ctx, id); err != nil && !stderrors.Is(err, cache.ErrNotFound) {
		return translate(err, "removing session record")
	}

	return nil
}

// Close releases the cache.
func (b *Backend[T]) Close() error {
	return b.cache.Close()
}

// translate maps a cache error onto the Backend contract.
//
// cache.ErrNotFound becomes sessions.ErrNotFound so that a Store can tell an
// absent session from a broken store without knowing which backend it holds.
// cache.ErrUnavailable is deliberately not folded into it: a read that answers
// "no session" during an outage would sign every user out at once, which is the
// opposite of what a circuit breaker is for.
func translate(err error, description string) error {
	if stderrors.Is(err, cache.ErrNotFound) {
		return platformerrors.Wrap(sessions.ErrNotFound, description)
	}

	return platformerrors.Wrap(err, description)
}
