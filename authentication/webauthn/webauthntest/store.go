package webauthntest

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	"github.com/primandproper/platform-go/v13/identifiers"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	// liveTTL is the TTL for every case that is not about expiry. It is long
	// enough that no store, however loaded, can expire a ceremony in the middle
	// of a case that assumes it is still there.
	liveTTL = time.Minute

	// expiryTTL is the TTL the expiry case saves with, and expiryWait is how
	// long it then waits before asserting the state has lapsed. The ratio
	// between them is the whole tolerance the suite has for a slow host: four
	// times the window, so a host would have to stall for a second and a half
	// mid-case to produce a false pass.
	//
	// It is a single sleep rather than a poll because the only way to ask
	// whether the state is still there is Consume, which would remove it — the
	// first observation has to be the one after expiry. The window is therefore
	// wall clock this suite always spends, which is why it is measured in
	// hundreds of milliseconds rather than seconds: it lands in every package
	// that runs the suite, including under a mutation run that re-runs the
	// whole binary once per mutant.
	expiryTTL  = 500 * time.Millisecond
	expiryWait = 2 * time.Second

	// contenders is how many goroutines race to consume one challenge in the
	// case that proves exactly one of them wins.
	contenders = 8
)

// Factory builds the store under test. It is called once per subtest, so an
// implementation whose state lives inside the value gets a fresh one per case
// and cannot pass by inheriting another case's leftovers.
//
// A store whose backing outlives the value (a table, a redis server) needs no
// cleaning between subtests: every challenge the suite uses carries a unique
// suffix, so one server can serve every subtest, every parallel run, and every
// rerun without collisions.
type Factory func(tb testing.TB) webauthn.SessionStore

// Option declares where an implementation stops honoring the full SessionStore
// contract. Each one removes cases, so an implementation that declares nothing
// is held to all of it.
type Option func(*deviations)

// deviations is the set of declared departures from the full contract.
type deviations struct {
	racyConsume bool
}

// WithRacyConsume declares that this store's Consume is a read followed by a
// removal rather than one operation, so two callers consuming one challenge at
// the same instant may both be handed the ceremony.
//
// The cache store is this one, because the cache.Cache seam has no
// fetch-and-remove. It is a property of the deployment rather than a testing
// detail — the window is real, however small — so declaring it here is the
// implementation saying out loud what its doc already says in prose.
func WithRacyConsume() Option {
	return func(d *deviations) {
		d.racyConsume = true
	}
}

// Run asserts every behavior a webauthn.SessionStore owes its callers against
// the implementation newStore builds, as one parallel subtest per behavior.
//
// It takes a *testing.T rather than the testing.TB the factory takes because it
// runs subtests, which TB cannot: a failure has to name the behavior that
// broke, not just the implementation.
func Run(t *testing.T, newStore Factory, opts ...Option) {
	t.Helper()

	var d deviations
	for _, opt := range opts {
		if opt != nil {
			opt(&d)
		}
	}

	t.Run("hands back the ceremony it was given", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		store := newStore(t)
		want := newSession(uniqueChallenge("round-trip"))

		must.NoError(t, store.Save(ctx, want, liveTTL))

		got, err := store.Consume(ctx, want.Challenge)
		must.NoError(t, err)
		must.NotNil(t, got)

		// Every field a Finish reads, because a store that drops one produces a
		// ceremony that fails verification for a reason the caller cannot see:
		// the user handle decides who is registering, the allowed credential
		// IDs decide which passkey may answer, and the verification
		// requirement decides whether an unverified one is accepted.
		test.EqOp(t, want.Challenge, got.Challenge)
		test.EqOp(t, want.RelyingPartyID, got.RelyingPartyID)
		test.Eq(t, want.UserID, got.UserID)
		test.Eq(t, want.AllowedCredentialIDs, got.AllowedCredentialIDs)
		test.EqOp(t, want.UserVerification, got.UserVerification)
		test.True(t, want.Expires.Equal(got.Expires),
			test.Sprintf("saved %v, read %v", want.Expires, got.Expires))
	})

	t.Run("a challenge can be answered once", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		store := newStore(t)
		session := newSession(uniqueChallenge("once"))

		must.NoError(t, store.Save(ctx, session, liveTTL))

		_, err := store.Consume(ctx, session.Challenge)
		must.NoError(t, err)

		// The whole point of Consume being one method. A replayed assertion
		// arrives at a store that no longer has the ceremony, so the replay
		// fails on the challenge rather than on the signature.
		_, err = store.Consume(ctx, session.Challenge)
		test.ErrorIs(t, err, webauthn.ErrSessionNotFound)
	})

	t.Run("a challenge nobody issued is not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		store := newStore(t)

		_, err := store.Consume(ctx, uniqueChallenge("never-issued"))
		test.ErrorIs(t, err, webauthn.ErrSessionNotFound)
	})

	t.Run("an empty challenge is refused rather than looked up", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		store := newStore(t)

		_, err := store.Consume(ctx, "")
		test.ErrorIs(t, err, webauthn.ErrChallengeRequired)
	})

	t.Run("Save refuses what cannot be a ceremony", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		store := newStore(t)

		test.ErrorIs(t, store.Save(ctx, nil, liveTTL), webauthn.ErrNilSession)
		test.ErrorIs(t, store.Save(ctx, &webauthn.SessionData{}, liveTTL), webauthn.ErrChallengeRequired)

		// Zero is refused rather than read as "no expiry": ceremony state that
		// never expires is a challenge that can be answered next year.
		session := newSession(uniqueChallenge("ttl"))
		test.ErrorIs(t, store.Save(ctx, session, 0), webauthn.ErrNonPositiveTTL)
		test.ErrorIs(t, store.Save(ctx, session, -time.Second), webauthn.ErrNonPositiveTTL)

		// And none of the three wrote anything.
		_, err := store.Consume(ctx, session.Challenge)
		test.ErrorIs(t, err, webauthn.ErrSessionNotFound)
	})

	t.Run("distinct challenges do not collide", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		store := newStore(t)
		first, second := newSession(uniqueChallenge("first")), newSession(uniqueChallenge("second"))
		first.UserID, second.UserID = []byte("user-one"), []byte("user-two")

		must.NoError(t, store.Save(ctx, first, liveTTL))
		must.NoError(t, store.Save(ctx, second, liveTTL))

		got, err := store.Consume(ctx, first.Challenge)
		must.NoError(t, err)
		test.Eq(t, []byte("user-one"), got.UserID)

		// Consuming the first must not have touched the second, which is the
		// difference between a store and a variable.
		got, err = store.Consume(ctx, second.Challenge)
		must.NoError(t, err)
		test.Eq(t, []byte("user-two"), got.UserID)
	})

	t.Run("re-saving a challenge replaces the ceremony under it", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		store := newStore(t)
		session := newSession(uniqueChallenge("replaced"))
		session.UserID = []byte("before")

		must.NoError(t, store.Save(ctx, session, liveTTL))

		session.UserID = []byte("after")
		must.NoError(t, store.Save(ctx, session, liveTTL))

		got, err := store.Consume(ctx, session.Challenge)
		must.NoError(t, err)
		test.Eq(t, []byte("after"), got.UserID)
	})

	t.Run("ceremony state does not outlive its TTL", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		store := newStore(t)
		session := newSession(uniqueChallenge("expiry"))

		must.NoError(t, store.Save(ctx, session, expiryTTL))

		time.Sleep(expiryWait)

		// ErrSessionNotFound rather than ErrSessionExpired: a store that reads
		// an expiry column can tell them apart and a store whose entry is
		// simply gone cannot, so the contract is the one both can keep.
		// ErrSessionExpired wraps it, so an implementation that knows more may
		// still say so.
		_, err := store.Consume(ctx, session.Challenge)
		test.ErrorIs(t, err, webauthn.ErrSessionNotFound)
	})

	t.Run("exactly one of several racing consumers gets the ceremony", func(t *testing.T) {
		t.Parallel()

		if d.racyConsume {
			t.Skip("implementation declared WithRacyConsume: its Consume is a read followed by a removal, so two callers can be handed one ceremony")
		}

		ctx := t.Context()
		store := newStore(t)
		session := newSession(uniqueChallenge("contended"))

		must.NoError(t, store.Save(ctx, session, liveTTL))

		var (
			start   sync.WaitGroup
			done    sync.WaitGroup
			winners atomic.Int64
			losses  = make([]error, contenders)
		)

		start.Add(1)
		done.Add(contenders)

		for i := range contenders {
			go func() {
				defer done.Done()

				start.Wait()

				if _, err := store.Consume(ctx, session.Challenge); err == nil {
					winners.Add(1)
				} else {
					losses[i] = err
				}
			}()
		}

		start.Done()
		done.Wait()

		test.EqOp(t, int64(1), winners.Load())

		// The losers are told the ceremony is not there, not something else. A
		// caller that has to distinguish "somebody beat me to it" from "the
		// database is down" would be a caller reading error strings.
		for i, err := range losses {
			if err != nil {
				test.ErrorIs(t, err, webauthn.ErrSessionNotFound, test.Sprintf("contender %d", i))
			}
		}
	})
}

// newSession is one ceremony's worth of state, populated in every field a
// Finish reads so that a round trip has something to lose.
func newSession(challenge string) *webauthn.SessionData {
	return &webauthn.SessionData{
		Challenge:            challenge,
		RelyingPartyID:       "example.com",
		UserID:               []byte("user-handle"),
		AllowedCredentialIDs: [][]byte{[]byte("credential-one"), []byte("credential-two")},
		UserVerification:     protocol.VerificationPreferred,
		// Truncated to the microsecond every supported column type stores, so
		// that a round trip through one of them is comparable to the value that
		// went in. UTC for the same reason: Postgres hands a timestamp back in
		// the session's zone.
		Expires: time.Now().UTC().Add(liveTTL).Truncate(time.Microsecond),
	}
}

// uniqueChallenge suffixes a name so that subtests sharing one backend — a
// table, a redis server — cannot collide with each other, and a rerun cannot
// inherit a challenge an earlier run left behind.
func uniqueChallenge(name string) string {
	return "webauthntest_" + name + "_" + identifiers.New()
}
