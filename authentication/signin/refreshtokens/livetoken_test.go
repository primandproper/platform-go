package refreshtokens

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// mintWithAccessToken begins a login whose first access token is tokenID.
func mintWithAccessToken(tb testing.TB, store *SQLStore, familyID, tokenID string, ttl time.Duration) *signin.RefreshTokenIssuance {
	tb.Helper()

	issuance, err := issueFor(tb, store, testScope(), &signin.RefreshTokenRequest{
		TTL:             ttl,
		FamilyID:        familyID,
		SubjectID:       testSubject,
		ActiveAccountID: testAccount,
		AccessTokenID:   tokenID,
	})
	must.NoError(tb, err)

	return issuance
}

// rotateWithAccessToken is what an exchange does: spend secret and mint its
// successor, recording the access token minted beside it.
func rotateWithAccessToken(tb testing.TB, store *SQLStore, secret, tokenID string) *signin.RefreshTokenIssuance {
	tb.Helper()

	var issuance *signin.RefreshTokenIssuance

	must.NoError(tb, withTx(tb, store, func(tx database.Tx) error {
		spent, err := store.Redeem(tb.Context(), tx, testScope(), secret)
		if err != nil {
			return err
		}

		issuance, err = store.Issue(tb.Context(), tx, testScope(), &signin.RefreshTokenRequest{
			TTL:             testTTL,
			FamilyID:        spent.FamilyID,
			SignedInAt:      spent.SignedInAt,
			SubjectID:       spent.SubjectID,
			ActiveAccountID: spent.ActiveAccountID,
			AccessTokenID:   tokenID,
		})

		return err
	}))

	return issuance
}

// liveToken reads a login's current token on the writer, which is the executor
// signin.Service.CheckSignIn hands it.
func liveToken(tb testing.TB, store *SQLStore, scope tenancy.Scope, familyID string) (*signin.RefreshToken, error) {
	tb.Helper()

	return store.LiveToken(tb.Context(), store.db.Writer(), scope, familyID)
}

// runLiveTokenSuite is LiveToken's behavior on whatever database the store is
// built over. Every family is minted here, so the suite shares a table with
// anything else without reading its rows.
//
// It moves c, so the caller runs it where nothing else is reading that clock.
func runLiveTokenSuite(t *testing.T, store *SQLStore, c *fakeClock) {
	t.Helper()

	t.Run("answers the login's token and the access token minted with it", func(t *testing.T) {
		family := "family_live_" + identifiers.New()
		issued := mintWithAccessToken(t, store, family, "jti_first", time.Hour)

		live, err := liveToken(t, store, testScope(), family)
		must.NoError(t, err)

		test.EqOp(t, family, live.FamilyID)
		test.EqOp(t, testSubject, live.SubjectID)
		test.EqOp(t, "jti_first", live.AccessTokenID)
		test.True(t, issued.Token.ExpiresAt.Equal(live.ExpiresAt),
			test.Sprintf("issued %v, read %v", issued.Token.ExpiresAt, live.ExpiresAt))
		test.Nil(t, live.RedeemedAt)
		test.Nil(t, live.RevokedAt)
	})

	// The rotated case: the spent row still exists and still names the first
	// access token, and it is not the answer.
	t.Run("answers the successor once the login has rotated", func(t *testing.T) {
		family := "family_rotated_" + identifiers.New()
		first := mintWithAccessToken(t, store, family, "jti_before", time.Hour)

		rotateWithAccessToken(t, store, first.Secret, "jti_after")

		live, err := liveToken(t, store, testScope(), family)
		must.NoError(t, err)
		test.EqOp(t, "jti_after", live.AccessTokenID)
	})

	t.Run("answers that a revoked login has ended", func(t *testing.T) {
		family := "family_revoked_" + identifiers.New()
		first := mintWithAccessToken(t, store, family, "jti_revoked", time.Hour)
		rotateWithAccessToken(t, store, first.Secret, "jti_revoked_next")

		must.NoError(t, withTx(t, store, func(tx database.Tx) error {
			_, err := store.RevokeFamily(t.Context(), tx, testScope(), family)

			return err
		}))

		_, err := liveToken(t, store, testScope(), family)
		test.ErrorIs(t, err, signin.ErrSignInEnded)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	})

	// Nothing collects the row: the deadline alone ends it, as it does for an
	// exchange.
	t.Run("answers that a lapsed login has ended", func(t *testing.T) {
		family := "family_lapsed_" + identifiers.New()
		mintWithAccessToken(t, store, family, "jti_lapsed", time.Minute)

		_, err := liveToken(t, store, testScope(), family)
		must.NoError(t, err)

		c.advance(2 * time.Minute)

		_, err = liveToken(t, store, testScope(), family)
		test.ErrorIs(t, err, signin.ErrSignInEnded)
	})

	// A family identifier is not a secret, and one in another directory is
	// another login.
	t.Run("answers a login in another scope, or none, as ended", func(t *testing.T) {
		family := "family_scoped_" + identifiers.New()
		mintWithAccessToken(t, store, family, "jti_scoped", time.Hour)

		_, err := liveToken(t, store, tenancy.Of("tenant_elsewhere"), family)
		test.ErrorIs(t, err, signin.ErrSignInEnded)

		_, err = liveToken(t, store, testScope(), "family_never_"+identifiers.New())
		test.ErrorIs(t, err, signin.ErrSignInEnded)
	})

	t.Run("answers no access token for a mint that recorded none", func(t *testing.T) {
		family := "family_unrecorded_" + identifiers.New()
		mintWithAccessToken(t, store, family, "", time.Hour)

		live, err := liveToken(t, store, testScope(), family)
		must.NoError(t, err)
		test.EqOp(t, "", live.AccessTokenID)
	})
}

func TestSQLStore_LiveToken(T *testing.T) {
	T.Parallel()

	T.Run("on SQLite", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		runLiveTokenSuite(t, store, c)
	})

	T.Run("records the access token on the issued row", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issued := mintWithAccessToken(t, store, testFamilyID, "jti_issued", testTTL)
		test.EqOp(t, "jti_issued", issued.Token.AccessTokenID)

		spent, err := redeem(t, store, testScope(), issued.Secret)
		must.NoError(t, err)
		test.EqOp(t, "jti_issued", spent.AccessTokenID)
	})

	T.Run("refuses a read that names nothing", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := store.LiveToken(t.Context(), store.db.Writer(), testScope(), "")
		test.ErrorIs(t, err, ErrEmptyFamilyID)

		_, err = store.LiveToken(t.Context(), nil, testScope(), testFamilyID)
		test.ErrorIs(t, err, ErrNilExecutor)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}
