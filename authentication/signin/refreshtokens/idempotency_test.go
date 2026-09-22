package refreshtokens

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/idempotency"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The key a client mints once per logical exchange. It is a constant rather than
// a generated value because every assertion here is about two presentations
// carrying the same one.
const testKey = "exchange_01"

// exchange is one whole idempotent exchange in one transaction: spend, mint the
// successor into the same family, and record what was minted.
//
// It is a helper rather than three calls per test because the three are one
// operation — the retry branch is reachable only from a row that was spent and
// recorded together, which is precisely the invariant
// signin.Service.ExchangeRefreshToken holds and the one a test assembling the
// calls by hand is free to break silently.
//
// It commits on ErrRefreshTokenReused for the reason redeem does: the family
// revocation is written into the same transaction.
func exchange(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	secret, key string,
) (*signin.RefreshToken, *signin.RefreshTokenIssuance, error) {
	tb.Helper()

	var (
		spent     *signin.RefreshToken
		successor *signin.RefreshTokenIssuance
		reuse     error
	)

	err := withTx(tb, store, func(tx database.Tx) error {
		var txErr error

		spent, txErr = store.RedeemIdempotently(tb.Context(), tx, scope, secret, key)
		if platformerrors.Is(txErr, signin.ErrRefreshTokenReused) {
			reuse = txErr

			return nil
		}

		if txErr != nil {
			return txErr
		}

		if successor, txErr = store.Issue(tb.Context(), tx, scope, &signin.RefreshTokenRequest{
			TTL:             testTTL,
			FamilyID:        spent.FamilyID,
			SubjectID:       spent.SubjectID,
			ActiveAccountID: spent.ActiveAccountID,
			Administrative:  spent.Administrative,
		}); txErr != nil {
			return txErr
		}

		return store.RecordSuccessor(tb.Context(), tx, scope, secret, successor.Secret)
	})

	if reuse != nil {
		return nil, nil, reuse
	}

	return spent, successor, err
}

func TestSQLStore_RedeemIdempotently(T *testing.T) {
	T.Parallel()

	// The first exchange is Redeem, with one more column written. Nothing about
	// the answer changes, which is the property that makes this additive.
	T.Run("a first exchange answers exactly as Redeem does", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		spent, successor, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		must.NoError(t, err)
		must.NotNil(t, spent)
		must.NotNil(t, spent.RedeemedAt)
		must.NotNil(t, successor)

		test.EqOp(t, testFamilyID, spent.FamilyID)
		test.EqOp(t, testSubject, spent.SubjectID)
		test.EqOp(t, testAccount, spent.ActiveAccountID)
		test.NotEqOp(t, issuance.Secret, successor.Secret)
	})

	// The whole point. A client that never received the first answer presents
	// the same token with the same key and gets a working successor, rather than
	// having its login ended for having lost a packet.
	T.Run("a retry with the key that spent the token mints a fresh successor", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		_, first, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		must.NoError(t, err)

		spent, second, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		must.NoError(t, err)
		must.NotNil(t, spent)
		must.NotNil(t, second)

		// Fresh, not a replay: this store keeps no secret at rest to replay, and
		// a replay would hand a capturing attacker the token the client holds.
		test.NotEqOp(t, first.Secret, second.Secret)

		// Same login, so the successor is a successor rather than a second
		// sign-in.
		test.EqOp(t, testFamilyID, second.Token.FamilyID)

		// And the client ends up holding exactly one live refresh token.
		redeemed, err := redeem(t, store, testScope(), second.Secret)
		must.NoError(t, err)
		test.EqOp(t, testFamilyID, redeemed.FamilyID)
	})

	// The half of the re-mint that is easy to leave out. Two live tokens in one
	// family is the property rotation exists to hold, given up by the mechanism
	// meant to make rotation survivable.
	T.Run("the superseded successor is revoked, and the family is not", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		_, first, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		must.NoError(t, err)

		_, second, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		must.NoError(t, err)

		// The token the lost response carried is dead, and dead as an ordinary
		// refusal rather than as a detected theft — nobody did anything wrong.
		_, err = redeem(t, store, testScope(), first.Secret)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.False(t, platformerrors.Is(err, signin.ErrRefreshTokenReused))

		// And the family survived it, which is the difference between this and
		// the reuse branch.
		_, err = redeem(t, store, testScope(), second.Secret)
		test.NoError(t, err)
	})

	// The bound that keeps this from granting capability that does not exist
	// today: one captured request must not become a renewable session.
	T.Run("a second retry with the same key is a reuse", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		_, _, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		must.NoError(t, err)

		_, second, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		must.NoError(t, err)

		_, _, err = exchange(t, store, testScope(), issuance.Secret, testKey)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)

		// And the reuse ended the login, successor included — the ordinary
		// answer, reached because the evidence had already been spent once.
		_, err = redeem(t, store, testScope(), second.Secret)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	})

	// The condition doing the real work, and the one that closes the window on
	// progress rather than on a clock.
	T.Run("a retry after the successor was spent is a reuse", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		_, successor, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		must.NoError(t, err)

		// The client received it after all.
		_, err = redeem(t, store, testScope(), successor.Secret)
		must.NoError(t, err)

		_, _, err = exchange(t, store, testScope(), issuance.Secret, testKey)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)
	})

	// The backstop, both sides of it. The clock only moves when this test moves
	// it, so the boundary is exact rather than approached.
	T.Run("a retry inside the grace window is honored", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		issuance := issue(t, store)

		_, _, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		must.NoError(t, err)

		c.advance(RemintGrace - time.Second)

		_, successor, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		test.NoError(t, err)
		must.NotNil(t, successor)
	})

	T.Run("a retry at the grace window's edge is a reuse", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		issuance := issue(t, store)

		_, _, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		must.NoError(t, err)

		// Exactly the boundary, which is closed against the retry: the window is
		// "less than RemintGrace has passed", so the instant it is reached is
		// outside it.
		c.advance(RemintGrace)

		_, _, err = exchange(t, store, testScope(), issuance.Secret, testKey)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)
	})

	// A thief's replay of a captured token carries no key, or somebody else's.
	// Either is the answer rotation has always given.
	T.Run("a presentation with a different key is a reuse", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		_, _, err := exchange(t, store, testScope(), issuance.Secret, testKey)
		must.NoError(t, err)

		_, _, err = exchange(t, store, testScope(), issuance.Secret, "exchange_02")
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)
	})

	T.Run("a token spent without a key is not retryable with one", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		// The ordinary exchange, which records no key at all.
		_, err := redeem(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)

		_, _, err = exchange(t, store, testScope(), issuance.Secret, testKey)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)
	})

	// Everything that is not a spent row is the refusal Redeem gives, and none
	// of it ends a family. A key changes what a later presentation means and
	// changes nothing about which caller may spend a token now.
	T.Run("the refusals a key does not change", func(t *testing.T) {
		t.Parallel()

		// Each case is the token to present and the directory to present it in,
		// so the one case that differs in the second is a row in the table
		// rather than a branch inside the body.
		for name, setUp := range map[string]func(*testing.T, *SQLStore) (tenancy.Scope, string){
			"an unknown token": func(*testing.T, *SQLStore) (tenancy.Scope, string) {
				return testScope(), "not a token this store ever minted"
			},
			"a revoked token": func(t *testing.T, store *SQLStore) (tenancy.Scope, string) {
				t.Helper()

				issuance := issue(t, store)
				_, err := revokeFamily(t, store, testScope(), testFamilyID)
				must.NoError(t, err)

				return testScope(), issuance.Secret
			},
			"another directory's token": func(t *testing.T, store *SQLStore) (tenancy.Scope, string) {
				t.Helper()

				return tenancy.Of("tenant_b"), issue(t, store).Secret
			},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				store, _ := newTestStore(t)
				scope, secret := setUp(t, store)

				_, _, err := exchange(t, store, scope, secret, testKey)
				test.ErrorIs(t, err, signin.ErrInvalidCredentials)
				test.False(t, platformerrors.Is(err, signin.ErrRefreshTokenReused))
			})
		}
	})

	// A malformed key is refused rather than dropped. Ignoring it would leave a
	// client believing its retry was protected by something the store never
	// recorded.
	T.Run("the key's own shape is refused through idempotency's sentinels", func(t *testing.T) {
		t.Parallel()

		for name, tc := range map[string]struct {
			err error
			key string
		}{
			"empty":        {key: "", err: idempotency.ErrKeyRequired},
			"over-long":    {key: string(make([]byte, MaximumIdempotencyKeyLength+1)), err: idempotency.ErrKeyTooLong},
			"unprintable":  {key: "exchange\x00one", err: idempotency.ErrKeyInvalid},
			"with a space": {key: "exchange one", err: idempotency.ErrKeyInvalid},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				store, _ := newTestStore(t)
				issuance := issue(t, store)

				_, _, err := exchange(t, store, testScope(), issuance.Secret, tc.key)
				test.ErrorIs(t, err, tc.err)

				// And the token is untouched, so the client can fix its header
				// and try again.
				_, err = redeem(t, store, testScope(), issuance.Secret)
				test.NoError(t, err)
			})
		}
	})

	// The argument refusals this shares with Redeem, which are checked before
	// the key is.
	T.Run("an empty secret is refused before the key is read", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, _, err := exchange(t, store, testScope(), "", testKey)
		test.ErrorIs(t, err, ErrEmptySecret)
	})
}

func TestSQLStore_RecordSuccessor(T *testing.T) {
	T.Parallel()

	T.Run("a predecessor this store has no row for is refused", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		err := withTx(t, store, func(tx database.Tx) error {
			return store.RecordSuccessor(t.Context(), tx, testScope(),
				"not a token this store ever minted", issuance.Secret)
		})
		test.ErrorIs(t, err, ErrSuccessorNotRecorded)
	})

	T.Run("a record made in another directory reaches nothing", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		err := withTx(t, store, func(tx database.Tx) error {
			return store.RecordSuccessor(t.Context(), tx, tenancy.Of("tenant_b"),
				issuance.Secret, "successor")
		})
		test.ErrorIs(t, err, ErrSuccessorNotRecorded)
	})

	T.Run("neither secret may be empty", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		for name, tc := range map[string]struct{ predecessor, successor string }{
			"no predecessor": {predecessor: "", successor: "successor"},
			"no successor":   {predecessor: "predecessor", successor: ""},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				err := withTx(t, store, func(tx database.Tx) error {
					return store.RecordSuccessor(t.Context(), tx, testScope(), tc.predecessor, tc.successor)
				})
				test.ErrorIs(t, err, ErrEmptySecret)
			})
		}
	})

	// A row spent but never recorded cannot name what it minted, so its own
	// client's retry has nothing to revoke. It is refused rather than
	// re-minted: honoring it would leave the earlier successor live.
	T.Run("a spend recorded against nothing is not retryable", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		// The spend alone — RedeemIdempotently without the RecordSuccessor the
		// interface pairs it with.
		err := withTx(t, store, func(tx database.Tx) error {
			_, redeemErr := store.RedeemIdempotently(t.Context(), tx, testScope(), issuance.Secret, testKey)

			return redeemErr
		})
		must.NoError(t, err)

		_, _, err = exchange(t, store, testScope(), issuance.Secret, testKey)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)
	})
}
