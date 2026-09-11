package links

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/cryptography/hashing"
	"github.com/primandproper/primitives-go/cryptography/hashing/sha256"
	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewMinter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		m, err := NewMinter(newMemoryStore(), WithAction(testAction, testPolicy()))
		must.NoError(t, err)
		test.NotNil(t, m)
	})

	T.Run("rejects a nil store", func(t *testing.T) {
		t.Parallel()

		_, err := NewMinter(nil, WithAction(testAction, testPolicy()))
		test.ErrorIs(t, err, ErrNilStore)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("rejects a Minter with no actions", func(t *testing.T) {
		t.Parallel()

		_, err := NewMinter(newMemoryStore())
		test.ErrorIs(t, err, ErrNoActions)
	})

	T.Run("rejects an action with an invalid policy", func(t *testing.T) {
		t.Parallel()

		_, err := NewMinter(newMemoryStore(), WithAction(testAction, ActionPolicy{
			URL: "https://app.example.com/auth/magic/{token}",
		}))
		test.ErrorIs(t, err, ErrInvalidTTL)
	})

	T.Run("reports the registered actions", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t, WithAction("verify_email", ActionPolicy{
			URL: "https://app.example.com/verify?t={token}",
			TTL: time.Hour,
		}))

		test.SliceLen(t, 2, m.Actions())
		test.SliceContains(t, m.Actions(), testAction)
	})
}

func TestMinter_Mint(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		test.EqOp(t, testAction, link.Action)
		test.EqOp(t, testSubject, link.Subject)
		test.True(t, strings.HasPrefix(link.URL, "https://app.example.com/auth/magic/"))
		test.StrHasSuffix(t, string(link.Token), link.URL)
		test.NotEq(t, "", string(link.ID))
	})

	T.Run("the ID is the digest of the token and not the token", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		// The relationship the whole design rests on: what is stored and spoken
		// about is derived from the secret and does not contain it.
		test.EqOp(t, ID(hashing.HexString(sha256.NewSHA256Hasher(), string(link.Token))), link.ID)
		test.False(t, strings.Contains(string(link.ID), string(link.Token)))
	})

	T.Run("stores no token anywhere in the record", func(t *testing.T) {
		t.Parallel()

		m, store := newTestMinterStore(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		// Read back under the ID, which is itself the assertion that the store
		// is keyed by the digest.
		record := store.stored(t, link.ID)

		test.EqOp(t, StateActive, record.State)
		test.EqOp(t, testAction, record.Action)
		test.EqOp(t, testSubject, record.Subject)
		test.False(t, strings.Contains(rendered(record), string(link.Token)))
	})

	T.Run("sets the purge deadline past the link's expiry", func(t *testing.T) {
		t.Parallel()

		c := newTestClock()
		m, store := newTestMinterStore(t, WithClock(c.Clock()), WithRetention(time.Hour))

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		// The gap is what buys "this link has already been used" over "no such
		// link" — the store may forget the record only after it.
		test.EqOp(t, link.ExpiresAt.Add(time.Hour), store.stored(t, link.ID).PurgeAfter)
	})

	// The absence of a resolution is nil rather than an instant nobody wrote.
	// A zero time.Time here would read as a stamp everywhere the record is
	// rendered, and the column the shipped store keeps it in is NULL for
	// exactly the rows this one is.
	T.Run("a freshly minted record carries no resolution stamp", func(t *testing.T) {
		t.Parallel()

		m, store := newTestMinterStore(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		test.Nil(t, store.stored(t, link.ID).ResolvedAt)
	})

	T.Run("mints two different tokens for the same subject", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		first, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		second, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		test.NotEq(t, string(first.Token), string(second.Token))
		test.NotEq(t, string(first.ID), string(second.ID))
	})

	T.Run("rejects an unregistered action", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		_, err := m.Mint(t.Context(), "magic_logn", testSubject)
		test.ErrorIs(t, err, ErrUnknownAction)
	})

	T.Run("rejects an empty subject", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		_, err := m.Mint(t.Context(), testAction, "")
		test.ErrorIs(t, err, ErrEmptySubject)
	})

	T.Run("expires at the action's TTL", func(t *testing.T) {
		t.Parallel()

		c := newTestClock()
		m := newTestMinter(t, WithClock(c.Clock()))

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		test.EqOp(t, c.Clock().Now().UTC().Add(testActionTTL), link.ExpiresAt)
	})

	T.Run("a per-call TTL overrides the action's", func(t *testing.T) {
		t.Parallel()

		c := newTestClock()
		m := newTestMinter(t, WithClock(c.Clock()))

		link, err := m.Mint(t.Context(), testAction, testSubject, WithTTL(time.Minute))
		must.NoError(t, err)

		test.EqOp(t, c.Clock().Now().UTC().Add(time.Minute), link.ExpiresAt)
	})

	T.Run("puts the token in a query template", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t, WithAction("unsubscribe", ActionPolicy{
			URL: "https://app.example.com/unsubscribe?t={token}",
			TTL: 365 * 24 * time.Hour,
		}))

		link, err := m.Mint(t.Context(), "unsubscribe", testSubject)
		must.NoError(t, err)

		test.EqOp(t, "https://app.example.com/unsubscribe?t="+string(link.Token), link.URL)
	})

	T.Run("reports a store failure rather than a link nothing recorded", func(t *testing.T) {
		t.Parallel()

		m := newFailingStoreMinter(t, platformerrors.New("the database is on fire"))

		_, err := m.Mint(t.Context(), testAction, testSubject)
		test.ErrorIs(t, err, ErrStoreUnavailable)
	})
}

func TestMinter_Redeem(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject, WithMetadata(map[string]string{"next": "/dashboard"}))
		must.NoError(t, err)

		claims, err := m.Redeem(t.Context(), link.Token)
		must.NoError(t, err)

		test.EqOp(t, testAction, claims.Action)
		test.EqOp(t, testSubject, claims.Subject)
		test.EqOp(t, link.ID, claims.ID)
		test.EqOp(t, link.ExpiresAt, claims.ExpiresAt)
		test.EqOp(t, "/dashboard", claims.Metadata["next"])
	})

	T.Run("a second redemption is refused", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		_, err = m.Redeem(t.Context(), link.Token)
		must.NoError(t, err)

		_, err = m.Redeem(t.Context(), link.Token)
		test.ErrorIs(t, err, ErrLinkAlreadyRedeemed)
	})

	T.Run("exactly one of many concurrent redemptions succeeds", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		const attempts = 16

		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			redeemed int
		)

		start := make(chan struct{})

		for range attempts {
			wg.Go(func() {
				<-start

				if _, redeemErr := m.Redeem(context.WithoutCancel(t.Context()), link.Token); redeemErr == nil {
					mu.Lock()
					redeemed++
					mu.Unlock()
				}
			})
		}

		close(start)
		wg.Wait()

		// The Store contract, from above it: whatever a store buys atomicity
		// with, exactly one caller may be told it holds the link. Without it
		// this is a number greater than one, and only under concurrency —
		// which is what links/database's guarded UPDATE is tested for in its
		// own package.
		test.EqOp(t, 1, redeemed)
	})

	T.Run("refuses a link past its expiry", func(t *testing.T) {
		t.Parallel()

		c := newTestClock()
		m := newTestMinter(t, WithClock(c.Clock()))

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		c.Advance(testActionTTL + time.Second)

		_, err = m.Redeem(t.Context(), link.Token)
		test.ErrorIs(t, err, ErrLinkExpired)
	})

	T.Run("refuses a link at the instant it expires", func(t *testing.T) {
		t.Parallel()

		c := newTestClock()
		m := newTestMinter(t, WithClock(c.Clock()))

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		c.Advance(testActionTTL)

		// The boundary belongs to the dead side: ExpiresAt is when it stops
		// working, not the last moment it works.
		_, err = m.Redeem(t.Context(), link.Token)
		test.ErrorIs(t, err, ErrLinkExpired)
	})

	T.Run("refuses an unknown token", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		_, err := m.Redeem(t.Context(), "not-a-token-anybody-minted")
		test.ErrorIs(t, err, ErrLinkNotFound)
	})

	T.Run("refuses an empty token without touching the store", func(t *testing.T) {
		t.Parallel()

		m := newFailingStoreMinter(t, platformerrors.New("should not be reached"))

		_, err := m.Redeem(t.Context(), "")
		test.ErrorIs(t, err, ErrInvalidToken)
	})

	T.Run("refuses an over-long token without hashing it", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t, WithMaxTokenLength(16))

		_, err := m.Redeem(t.Context(), Token(strings.Repeat("a", 17)))
		test.ErrorIs(t, err, ErrInvalidToken)
	})

	T.Run("fails closed when the consuming write cannot land", func(t *testing.T) {
		t.Parallel()

		// A store that mints fine and cannot resolve: the link is valid and
		// cannot be marked spent. Handing back claims here would be single use
		// failing open, which is the one thing that must not happen.
		m, store := newTestMinterStore(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		store.resolveErr = platformerrors.New("the database is on fire")

		claims, err := m.Redeem(t.Context(), link.Token)
		test.ErrorIs(t, err, ErrStoreUnavailable)
		test.Nil(t, claims)
	})

	T.Run("fails closed when the store cannot be read", func(t *testing.T) {
		t.Parallel()

		m := newFailingStoreMinter(t, platformerrors.New("the database is on fire"))

		_, err := m.Redeem(t.Context(), "some-token")
		test.ErrorIs(t, err, ErrStoreUnavailable)
	})

	T.Run("ignores a record written by another version", func(t *testing.T) {
		t.Parallel()

		m, store := newTestMinterStore(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		store.stored(t, link.ID).Version = RecordVersion + 1

		// A record this binary cannot read means the same thing to a bearer as
		// no record at all, and the store's ErrStaleRecord is what gets it
		// there rather than being reported as an outage.
		_, err = m.Redeem(t.Context(), link.Token)
		test.ErrorIs(t, err, ErrLinkNotFound)
	})

	T.Run("reports a record written by another version as absent to Inspect too", func(t *testing.T) {
		t.Parallel()

		m, store := newTestMinterStore(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		store.stored(t, link.ID).Version = RecordVersion + 1

		_, err = m.Inspect(t.Context(), link.Token)
		test.ErrorIs(t, err, ErrLinkNotFound)
	})

	T.Run("hands back a metadata map the store does not share", func(t *testing.T) {
		t.Parallel()

		m, store := newTestMinterStore(t)

		link, err := m.Mint(t.Context(), testAction, testSubject, WithMetadata(map[string]string{"next": "/dashboard"}))
		must.NoError(t, err)

		claims, err := m.Redeem(t.Context(), link.Token)
		must.NoError(t, err)

		claims.Metadata["next"] = "/evil"

		// The store double hands back the pointer it holds, which a real store
		// is free to do, so a shared map would have let that assignment edit
		// the stored record.
		test.EqOp(t, "/dashboard", store.stored(t, link.ID).Metadata["next"])
	})
}

func TestMinter_Inspect(T *testing.T) {
	T.Parallel()

	T.Run("reports the claims without consuming the link", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		claims, err := m.Inspect(t.Context(), link.Token)
		must.NoError(t, err)
		test.EqOp(t, testSubject, claims.Subject)

		// The reason Inspect exists: a mail scanner's GET must leave the link
		// intact for the person the message was sent to.
		_, err = m.Redeem(t.Context(), link.Token)
		test.NoError(t, err)
	})

	T.Run("reports the same refusals redemption would", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		_, err = m.Redeem(t.Context(), link.Token)
		must.NoError(t, err)

		_, err = m.Inspect(t.Context(), link.Token)
		test.ErrorIs(t, err, ErrLinkAlreadyRedeemed)
	})

	T.Run("refuses an unknown token", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		_, err := m.Inspect(t.Context(), "not-a-token-anybody-minted")
		test.ErrorIs(t, err, ErrLinkNotFound)
	})

	T.Run("refuses an empty token", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		_, err := m.Inspect(t.Context(), "")
		test.ErrorIs(t, err, ErrInvalidToken)
	})
}

func TestMinter_Revoke(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		must.NoError(t, m.Revoke(t.Context(), link.ID))

		_, err = m.Redeem(t.Context(), link.Token)
		test.ErrorIs(t, err, ErrLinkRevoked)
	})

	T.Run("revoking twice is not an error", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		must.NoError(t, m.Revoke(t.Context(), link.ID))
		test.NoError(t, m.Revoke(t.Context(), link.ID))
	})

	T.Run("reports that a revoked link had already been used", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		_, err = m.Redeem(t.Context(), link.Token)
		must.NoError(t, err)

		// An operator revoking after a suspected compromise has to learn they
		// were too late; a nil here would say the opposite.
		test.ErrorIs(t, m.Revoke(t.Context(), link.ID), ErrLinkAlreadyRedeemed)
	})

	T.Run("refuses an empty ID", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		test.ErrorIs(t, m.Revoke(t.Context(), ""), ErrInvalidID)
	})

	T.Run("reports an unknown ID", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		test.ErrorIs(t, m.Revoke(t.Context(), "0000"), ErrLinkNotFound)
	})

	T.Run("an expired link needs no revoking", func(t *testing.T) {
		t.Parallel()

		c := newTestClock()
		m := newTestMinter(t, WithClock(c.Clock()))

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		c.Advance(testActionTTL + time.Second)

		test.NoError(t, m.Revoke(t.Context(), link.ID))
	})
}

// secondAction is a second registered flow, for the cases about a revocation
// that has to cross actions because the caller does not know what was minted.
const secondAction Action = "verify_email"

// secondPolicy is secondAction's policy, at a URL of its own so that a link
// minted under it is distinguishable from a testAction link by sight.
func secondPolicy() ActionPolicy {
	return ActionPolicy{
		URL: "https://app.example.com/auth/verify/{token}",
		TTL: testActionTTL,
	}
}

func TestMinter_RevokeForSubject(T *testing.T) {
	T.Parallel()

	// The whole point: the caller names the person, not the links, and gets
	// every flow's links rather than the one they happened to think of.
	T.Run("withdraws every live link for the subject, across actions", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t, WithAction(secondAction, secondPolicy()))

		first, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		second, err := m.Mint(t.Context(), secondAction, testSubject)
		must.NoError(t, err)

		revoked, err := m.RevokeForSubject(t.Context(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, int64(2), revoked)

		_, err = m.Redeem(t.Context(), first.Token)
		test.ErrorIs(t, err, ErrLinkRevoked)

		_, err = m.Redeem(t.Context(), second.Token)
		test.ErrorIs(t, err, ErrLinkRevoked)
	})

	T.Run("leaves another subject's links alone", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		mine, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		theirs, err := m.Mint(t.Context(), testAction, "user_456")
		must.NoError(t, err)

		revoked, err := m.RevokeForSubject(t.Context(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		_, err = m.Redeem(t.Context(), mine.Token)
		test.ErrorIs(t, err, ErrLinkRevoked)

		claims, err := m.Redeem(t.Context(), theirs.Token)
		must.NoError(t, err)
		test.EqOp(t, Subject("user_456"), claims.Subject)
	})

	// The method takes no tenancy.Scope and there is nothing in a Record that
	// could narrow one, so a subject's links are withdrawn wherever they were
	// minted from. An application that records a tenant against a link records
	// it in metadata, and metadata is not a predicate.
	T.Run("crosses whatever tenants the subject belongs to", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		for _, tenant := range []string{"tenant_a", "tenant_b", "tenant_c"} {
			_, err := m.Mint(t.Context(), testAction, testSubject,
				WithMetadata(map[string]string{"tenant": tenant}))
			must.NoError(t, err)
		}

		revoked, err := m.RevokeForSubject(t.Context(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, int64(3), revoked)
	})

	T.Run("leaves an already-resolved link where it is", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		spent, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		_, err = m.Redeem(t.Context(), spent.Token)
		must.NoError(t, err)

		revoked, err := m.RevokeForSubject(t.Context(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)

		// Still "already redeemed" rather than "revoked": the revocation did
		// not reach a row somebody had already spent, and the sentence a second
		// click gets is the true one.
		_, err = m.Redeem(t.Context(), spent.Token)
		test.ErrorIs(t, err, ErrLinkAlreadyRedeemed)
	})

	// Documented behavior rather than an accident. Nothing in this package
	// decides liveness in SQL, so the write matches on the resolution stamp
	// alone and an expired link that nobody ever resolved is moved too.
	T.Run("withdraws a link that expired without being resolved", func(t *testing.T) {
		t.Parallel()

		c := newTestClock()
		m := newTestMinter(t, WithClock(c.Clock()))

		link, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		c.Advance(testActionTTL + time.Second)

		revoked, err := m.RevokeForSubject(t.Context(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		_, err = m.Redeem(t.Context(), link.Token)
		test.ErrorIs(t, err, ErrLinkRevoked)
	})

	T.Run("a second call finds nothing left to withdraw", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		_, err := m.Mint(t.Context(), testAction, testSubject)
		must.NoError(t, err)

		revoked, err := m.RevokeForSubject(t.Context(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		revoked, err = m.RevokeForSubject(t.Context(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)
	})

	T.Run("refuses an empty subject", func(t *testing.T) {
		t.Parallel()

		m := newTestMinter(t)

		revoked, err := m.RevokeForSubject(t.Context(), "")
		test.ErrorIs(t, err, ErrEmptySubject)
		test.EqOp(t, int64(0), revoked)
	})

	// A store that cannot be reached fails closed, as an outage rather than as
	// a revocation that quietly moved nothing.
	T.Run("fails closed when the store cannot be written", func(t *testing.T) {
		t.Parallel()

		m := newFailingStoreMinter(t, platformerrors.New("the database is on fire"))

		revoked, err := m.RevokeForSubject(t.Context(), testSubject)
		test.ErrorIs(t, err, ErrStoreUnavailable)
		test.EqOp(t, int64(0), revoked)
	})
}

// newFailingStoreMinter builds a Minter whose store fails every operation, for
// the cases that must not degrade into a working-looking answer.
func newFailingStoreMinter(tb testing.TB, storeErr error) *Minter {
	tb.Helper()

	store := newMemoryStore()
	store.getErr, store.putErr, store.resolveErr, store.revokeErr = storeErr, storeErr, storeErr, storeErr

	m, err := NewMinter(store, WithAction(testAction, testPolicy()))
	must.NoError(tb, err)

	return m
}

// rendered flattens a record into one string, so a test can assert that a
// secret appears nowhere in it rather than field by field.
func rendered(record *Record) string {
	parts := []string{
		record.CreatedAt.String(),
		record.ExpiresAt.String(),
		string(record.Action),
		string(record.Subject),
	}

	// An active link carries no resolution stamp, and the record this flattens
	// is usually one, so the field is appended rather than dereferenced.
	if record.ResolvedAt != nil {
		parts = append(parts, record.ResolvedAt.String())
	}

	for k, v := range record.Metadata {
		parts = append(parts, k, v)
	}

	return strings.Join(parts, "|")
}
