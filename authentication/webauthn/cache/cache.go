/*
Package cache stores WebAuthn ceremony state in a cache.Cache.

It is the store for a deployment that has redis and no relational database.
Ceremony state is small, lives for about a minute, and is discarded on use,
which is the shape a cache is best at — and unlike the session table, nothing
here has to be swept, because the cache reclaims its own entries.

# Where it stops

Consume is a read followed by a delete, not one operation, because the
cache.Cache seam has nothing that fetches and removes atomically. Two requests
answering the same challenge in the same instant can therefore both be handed
the ceremony, where authentication/webauthn/database hands it to exactly one.
The window is the microseconds between the two round trips, and what fits in it
is a replay of an assertion the legitimate user just made — so the practical
cost is that one authentication may be counted twice, not that a stale
assertion becomes usable later. A deployment that wants the stronger guarantee
uses the database store, and this package declares the deviation to the
conformance suite rather than quietly passing it.

# A memory cache is not a deployment

Backed by cache/memory this store is per-process, which is exactly the failure
the WebAuthn ceremony is prone to: the challenge is issued by one replica and
answered on another, and the login fails for no reason the user can act on. Use
redis, or use the database store. Memory is for tests and for a single-process
service.
*/
package cache

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/observability"
)

// serviceName names the loggers and spans this store emits.
const serviceName = "webauthn_cache"

// challengeKey is the span and log key the ceremony's challenge is recorded
// under, matching the key the relying party records it under, so that a Begin
// and its Finish join on one field.
const challengeKey = "webauthn.challenge"

// SessionStore keeps WebAuthn ceremony state in a cache.
//
// It is exported, and returned by NewSessionStore, so a caller who has chosen
// cache-backed ceremony state can depend on that choice rather than on the
// webauthn.SessionStore seam.
type SessionStore struct {
	cache cache.Cache[webauthn.SessionData]
	o11y  observability.Observer
}

var _ webauthn.SessionStore = (*SessionStore)(nil)

// NewSessionStore builds a SessionStore over a cache.
//
// The cache is required and has no default, because which one it is decides
// whether the store works at all: see the package documentation on why a memory
// cache is not a deployment.
//
// The cache's own default expiry is never used — every write carries the
// ceremony's remaining time — so a cache built solely for this can be
// configured with any expiry at all.
func NewSessionStore(c cache.Cache[webauthn.SessionData], opts ...Option) (*SessionStore, error) {
	if c == nil {
		return nil, ErrNilCache
	}

	o := newOptions(opts)

	return &SessionStore{
		cache: c,
		o11y:  observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}, nil
}

// Save stores a ceremony's state under its own challenge for ttl.
func (s *SessionStore) Save(ctx context.Context, session *webauthn.SessionData, ttl time.Duration) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := webauthn.ValidateSession(session, ttl); err != nil {
		return err
	}

	op.Set(challengeKey, session.Challenge)

	if err := s.cache.Set(ctx, session.Challenge, session, cache.WithExpiry(ttl)); err != nil {
		return op.Error(err, "storing webauthn ceremony session")
	}

	return nil
}

// Consume returns the state stored under challenge and removes it.
//
// The delete is not conditional on the read having won a race, because the
// cache seam offers no way to make it so. It is, however, unconditional in the
// other direction: the entry is removed even though this caller may not be the
// only one holding it, so a replay arriving a moment later still finds nothing.
func (s *SessionStore) Consume(ctx context.Context, challenge string) (*webauthn.SessionData, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if challenge == "" {
		return nil, webauthn.ErrChallengeRequired
	}

	op.Set(challengeKey, challenge)

	session, err := s.cache.Get(ctx, challenge)
	if err != nil {
		if stderrors.Is(err, cache.ErrNotFound) {
			return nil, webauthn.ErrSessionNotFound
		}

		return nil, op.Error(err, "reading webauthn ceremony session")
	}

	// A provider that reports a miss as a nil value rather than as ErrNotFound
	// is answered the same way. Handing back a nil session with a nil error
	// would put the panic in the caller.
	if session == nil {
		return nil, webauthn.ErrSessionNotFound
	}

	if err = s.cache.Delete(ctx, challenge); err != nil && !stderrors.Is(err, cache.ErrNotFound) {
		// Returned, not logged and shrugged off. A delete that failed is a
		// challenge that can be answered again until it expires, and this is
		// the one operation in this package that is supposed to prevent that;
		// refusing the ceremony costs the user a retry, which is the cheaper
		// half of the trade.
		return nil, op.Error(err, "removing webauthn ceremony session")
	}

	return session, nil
}
