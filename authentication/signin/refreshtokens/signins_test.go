package refreshtokens

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// mintInto issues a token into a named login for a named subject, failing the
// test if it cannot.
func mintInto(tb testing.TB, store *SQLStore, scope tenancy.Scope, familyID, subjectID string) *signin.RefreshTokenIssuance {
	tb.Helper()

	issuance, err := issueFor(tb, store, scope, &signin.RefreshTokenRequest{
		TTL:             testTTL,
		FamilyID:        familyID,
		SubjectID:       subjectID,
		ActiveAccountID: testAccount,
	})
	must.NoError(tb, err)

	return issuance
}

// rotate spends a token and mints its successor into the same login, which is
// what an exchange does — carrying the login's start forward, as the service
// does.
func rotate(tb testing.TB, store *SQLStore, scope tenancy.Scope, secret string) *signin.RefreshTokenIssuance {
	tb.Helper()

	var issuance *signin.RefreshTokenIssuance

	must.NoError(tb, withTx(tb, store, func(tx database.Tx) error {
		spent, err := store.Redeem(tb.Context(), tx, scope, secret)
		if err != nil {
			return err
		}

		issuance, err = store.Issue(tb.Context(), tx, scope, &signin.RefreshTokenRequest{
			TTL:             testTTL,
			FamilyID:        spent.FamilyID,
			SignedInAt:      spent.SignedInAt,
			SubjectID:       spent.SubjectID,
			ActiveAccountID: spent.ActiveAccountID,
			Administrative:  spent.Administrative,
		})

		return err
	}))

	return issuance
}

// listSignIns reads a subject's live logins on the store's reader, which is the
// executor the service hands it.
func listSignIns(tb testing.TB, store *SQLStore, scope tenancy.Scope, subjectID string, limit uint16) []*signin.ActiveSignIn {
	tb.Helper()

	signIns, err := store.ListActiveSignIns(tb.Context(), store.db.Reader(), scope, subjectID, limit)
	must.NoError(tb, err)

	return signIns
}

// familyIDs is the order a listing named its logins in.
func familyIDs(signIns []*signin.ActiveSignIn) []string {
	ids := make([]string, 0, len(signIns))
	for _, s := range signIns {
		ids = append(ids, s.FamilyID)
	}

	return ids
}

func TestSQLStore_Issue_SignedInAt(T *testing.T) {
	T.Parallel()

	// A mint that begins a login names no start, and the login began at the
	// mint.
	T.Run("begins a login at the mint", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		issuance := issue(t, store)

		test.EqOp(t, c.Now().UTC(), issuance.Token.SignedInAt)
	})

	// A successor names the login's start, and every row in the family carries
	// it — which is what survives the first row being swept.
	T.Run("carries the login's start onto a successor", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		began := c.Now().UTC()
		first := issue(t, store)

		c.advance(time.Hour)

		second := rotate(t, store, testScope(), first.Secret)

		test.EqOp(t, began, second.Token.SignedInAt)
		test.EqOp(t, began.Add(time.Hour), second.Token.IssuedAt)

		spent, err := redeem(t, store, testScope(), second.Secret)
		must.NoError(t, err)
		test.EqOp(t, began, spent.SignedInAt)
	})
}

func TestSQLStore_ListActiveSignIns(T *testing.T) {
	T.Parallel()

	T.Run("answers one entry per live login, most recently refreshed first", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		began := c.Now().UTC()
		phone := mintInto(t, store, testScope(), "family_phone", testSubject)

		c.advance(time.Minute)
		mintInto(t, store, testScope(), "family_laptop", testSubject)

		// The phone refreshes twice after the laptop signs in. Each exchange
		// leaves a spent row behind, and none of them is a login of its own.
		c.advance(time.Minute)
		phone = rotate(t, store, testScope(), phone.Secret)
		c.advance(time.Minute)
		rotate(t, store, testScope(), phone.Secret)

		signIns := listSignIns(t, store, testScope(), testSubject, 10)
		must.SliceLen(t, 2, signIns)

		test.Eq(t, []string{"family_phone", "family_laptop"}, familyIDs(signIns))

		// The phone began first and refreshed last, and those are two facts.
		test.EqOp(t, began, signIns[0].SignedInAt)
		test.EqOp(t, began.Add(3*time.Minute), signIns[0].LastRefreshedAt)
		test.EqOp(t, began.Add(3*time.Minute).Add(testTTL), signIns[0].ExpiresAt)
		test.EqOp(t, testAccount, signIns[0].ActiveAccountID)
		test.False(t, signIns[0].Administrative)

		// A login that never refreshed last refreshed when it began.
		test.EqOp(t, began.Add(time.Minute), signIns[1].SignedInAt)
		test.EqOp(t, signIns[1].SignedInAt, signIns[1].LastRefreshedAt)
	})

	T.Run("reports which door a login came through", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := issueFor(t, store, testScope(), &signin.RefreshTokenRequest{
			TTL:            time.Hour,
			FamilyID:       testFamilyID,
			SubjectID:      testSubject,
			Administrative: true,
		})
		must.NoError(t, err)

		signIns := listSignIns(t, store, testScope(), testSubject, 10)
		must.SliceLen(t, 1, signIns)
		test.True(t, signIns[0].Administrative)
		test.EqOp(t, "", signIns[0].ActiveAccountID)
	})

	// Live is the exchange's reading: revoked and lapsed logins are gone from
	// the list whether or not anything has collected their rows.
	T.Run("leaves out ended logins", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		mintInto(t, store, testScope(), "family_revoked", testSubject)
		mintInto(t, store, testScope(), "family_live", testSubject)

		_, err := revokeFamily(t, store, testScope(), "family_revoked")
		must.NoError(t, err)

		_, err = issueFor(t, store, testScope(), &signin.RefreshTokenRequest{
			TTL:       time.Minute,
			FamilyID:  "family_lapsing",
			SubjectID: testSubject,
		})
		must.NoError(t, err)

		c.advance(2 * time.Minute)

		test.Eq(t, []string{"family_live"}, familyIDs(listSignIns(t, store, testScope(), testSubject, 10)))
	})

	// A detected reuse ends the family, and the listing agrees.
	T.Run("leaves out a login a reuse ended", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		first := issue(t, store)
		rotate(t, store, testScope(), first.Secret)

		_, err := redeem(t, store, testScope(), first.Secret)
		must.ErrorIs(t, err, signin.ErrRefreshTokenReused)

		test.SliceEmpty(t, listSignIns(t, store, testScope(), testSubject, 10))
	})

	T.Run("answers only for the subject and the scope it was asked about", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		mintInto(t, store, testScope(), "family_mine", testSubject)
		mintInto(t, store, testScope(), "family_theirs", "user_02")
		mintInto(t, store, tenancy.Of("tenant_b"), "family_elsewhere", testSubject)

		test.Eq(t, []string{"family_mine"}, familyIDs(listSignIns(t, store, testScope(), testSubject, 10)))
		test.SliceEmpty(t, listSignIns(t, store, tenancy.Global(), testSubject, 10))
	})

	// The limit keeps the most recently refreshed, so what it leaves out is
	// what has been idle longest.
	T.Run("keeps the most recent under a limit", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		for _, family := range []string{"family_a", "family_b", "family_c"} {
			mintInto(t, store, testScope(), family, testSubject)
			c.advance(time.Minute)
		}

		test.Eq(t, []string{"family_c", "family_b"}, familyIDs(listSignIns(t, store, testScope(), testSubject, 2)))
	})

	T.Run("answers nobody's logins with an empty list", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		signIns := listSignIns(t, store, testScope(), "user_nobody", 10)
		test.NotNil(t, signIns)
		test.SliceEmpty(t, signIns)
	})

	// It is a read on the wider executor, so a caller inside a transaction sees
	// its own uncommitted mint.
	T.Run("reads its own transaction's writes", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		must.NoError(t, withTx(t, store, func(tx database.Tx) error {
			if _, err := store.Issue(t.Context(), tx, testScope(), &signin.RefreshTokenRequest{
				TTL: testTTL, FamilyID: testFamilyID, SubjectID: testSubject,
			}); err != nil {
				return err
			}

			signIns, err := store.ListActiveSignIns(t.Context(), tx, testScope(), testSubject, 10)
			if err != nil {
				return err
			}

			test.Eq(t, []string{testFamilyID}, familyIDs(signIns))

			return nil
		}))
	})

	T.Run("refuses a listing that names nothing", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := store.ListActiveSignIns(t.Context(), store.db.Reader(), testScope(), "", 10)
		test.ErrorIs(t, err, ErrEmptySubjectID)

		_, err = store.ListActiveSignIns(t.Context(), store.db.Reader(), testScope(), testSubject, 0)
		test.ErrorIs(t, err, ErrZeroLimit)

		_, err = store.ListActiveSignIns(t.Context(), nil, testScope(), testSubject, 10)
		test.ErrorIs(t, err, ErrNilExecutor)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}

func TestSQLStore_EndSignIns_OneOfTheSubjects(T *testing.T) {
	T.Parallel()

	T.Run("ends the named login and no other", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		phone := mintInto(t, store, testScope(), "family_phone", testSubject)
		rotate(t, store, testScope(), phone.Secret)
		laptop := mintInto(t, store, testScope(), "family_laptop", testSubject)

		ended, err := endSignIns(t, store, testScope(),
			signin.SignInSelector{SubjectID: testSubject, FamilyID: "family_phone"})
		must.NoError(t, err)

		test.Eq(t, []string{"family_phone"}, endedFamilies(ended))
		test.EqOp(t, testSubject, ended[0].SubjectID)

		// Two rows: the spent one and its live successor. Both end, so the
		// record says when the login stopped rather than when each row did.
		test.EqOp(t, 2, endedRevoked(ended))

		test.Eq(t, []string{"family_laptop"}, familyIDs(listSignIns(t, store, testScope(), testSubject, 10)))

		_, err = redeem(t, store, testScope(), laptop.Secret)
		test.NoError(t, err)
	})

	// The subject in the key is the whole of what makes this safe to hand a
	// signed-in caller: somebody else's family id reaches nothing.
	T.Run("cannot reach somebody else's login", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		theirs := mintInto(t, store, testScope(), "family_theirs", "user_02")

		ended, err := endSignIns(t, store, testScope(),
			signin.SignInSelector{SubjectID: testSubject, FamilyID: "family_theirs"})
		must.NoError(t, err)
		test.SliceEmpty(t, ended)

		_, err = redeem(t, store, testScope(), theirs.Secret)
		test.NoError(t, err)
	})

	T.Run("cannot reach its own login in another scope", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		elsewhere := mintInto(t, store, tenancy.Of("tenant_b"), testFamilyID, testSubject)

		ended, err := endSignIns(t, store, testScope(),
			signin.SignInSelector{SubjectID: testSubject, FamilyID: testFamilyID})
		must.NoError(t, err)
		test.SliceEmpty(t, ended)

		_, err = redeem(t, store, tenancy.Of("tenant_b"), elsewhere.Secret)
		test.NoError(t, err)
	})

	T.Run("reports nothing for a login already ended", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issue(t, store)

		selector := signin.SignInSelector{SubjectID: testSubject, FamilyID: testFamilyID}

		ended, err := endSignIns(t, store, testScope(), selector)
		must.NoError(t, err)
		test.EqOp(t, 1, endedRevoked(ended))

		ended, err = endSignIns(t, store, testScope(), selector)
		must.NoError(t, err)
		test.SliceEmpty(t, ended)
	})

	// A lapsed login is over already. Reporting it as ended would put a
	// sign-out in an audit trail for a login nobody could have used.
	T.Run("reports nothing for a login that lapsed", func(t *testing.T) {
		t.Parallel()

		store, clk := newTestStore(t)

		issue(t, store)
		clk.advance(testTTL + time.Second)

		ended, err := endSignIns(t, store, testScope(),
			signin.SignInSelector{SubjectID: testSubject, FamilyID: testFamilyID})
		must.NoError(t, err)
		test.SliceEmpty(t, ended)
	})
}

func TestSQLStore_EndSignIns_ByFamily(T *testing.T) {
	T.Parallel()

	// An operator ending a login by its id is not told whose it is, and the
	// hook it reports to has to be.
	T.Run("ends the login and says whose it was", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		ended := issue(t, store)
		other := mintInto(t, store, testScope(), "family_other", testSubject)

		got, err := endSignIns(t, store, testScope(), signin.SignInSelector{FamilyID: testFamilyID})
		must.NoError(t, err)
		must.SliceLen(t, 1, got)
		test.EqOp(t, testFamilyID, got[0].FamilyID)
		test.EqOp(t, testSubject, got[0].SubjectID)
		test.EqOp(t, 1, got[0].Revoked)

		_, err = redeem(t, store, testScope(), ended.Secret)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		_, err = redeem(t, store, testScope(), other.Secret)
		test.NoError(t, err)
	})

	T.Run("stays inside its directory", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issue(t, store)

		got, err := endSignIns(t, store, tenancy.Of("tenant_b"), signin.SignInSelector{FamilyID: testFamilyID})
		must.NoError(t, err)
		test.SliceEmpty(t, got)
	})

	T.Run("refuses a selection that names nothing", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := endSignIns(t, store, testScope(), signin.SignInSelector{})
		test.ErrorIs(t, err, ErrEmptySelector)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})
}
