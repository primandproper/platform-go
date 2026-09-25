package grants

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/grants/internal/grantsdb"

	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSQLStore_SQLite runs the behavioral suite against SQLite, which needs no
// container. The same suite runs against real servers in containers_test.go.
func TestSQLStore_SQLite(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is every behavior this store promises, against whatever database
// the environment holds. It is one function because it runs on three dialects,
// and a case outside it would be checked on one of them.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	runPutCases(t, env)
	runRefreshCases(t, env)
	runRevokeCases(t, env)
	runSubjectCases(t, env)
	runRefusalCases(t, env)
}

func runPutCases(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("put answers with the row it wrote, tokens opened", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		test.NotEqOp(t, "", grant.ID)
		test.EqOp(t, testScope, grant.Scope)
		test.EqOp(t, "studio_1", grant.Subject)
		test.EqOp(t, "google", grant.Provider)
		test.EqOp(t, "owner@example.com", grant.ProviderAccountID)
		test.Eq(t, []string{"openid", "https://www.googleapis.com/auth/calendar.events"}, grant.GrantedScopes)
		test.EqOp(t, "access-studio_1-google", grant.AccessToken)
		test.EqOp(t, "refresh-studio_1-google", grant.RefreshToken)
		must.NotNil(t, grant.AccessTokenExpiresAt)
		test.True(t, testExpiry.Equal(*grant.AccessTokenExpiresAt))
		test.False(t, grant.CreatedAt.IsZero())
		test.Nil(t, grant.RevokedAt)
		test.EqOp(t, RevocationReason(""), grant.RevocationReason)
	})

	t.Run("get round-trips through the encryptor", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		got, err := store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		must.NoError(t, err)
		test.EqOp(t, "access-studio_1-google", got.AccessToken)
		test.EqOp(t, "refresh-studio_1-google", got.RefreshToken)
	})

	t.Run("the tokens are ciphertext at rest", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		row, err := store.q.GetGrant(t.Context(), env.reader(), grantsdb.GetGrantParams{ID: grant.ID, Scope: testScope})
		must.NoError(t, err)

		test.False(t, bytes.Contains(row.AccessToken, []byte("access-studio_1-google")))
		test.False(t, bytes.Contains(row.RefreshToken, []byte("refresh-studio_1-google")))
		test.SliceNotEmpty(t, row.AccessToken)
		test.SliceNotEmpty(t, row.RefreshToken)
	})

	t.Run("a ciphertext moved to another row fails to open", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mine := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))
		theirs := env.mustPut(t, store, testScope, newConsent("studio_2", "google"))

		mineRow, err := store.q.GetGrant(t.Context(), env.reader(), grantsdb.GetGrantParams{ID: mine.ID, Scope: testScope})
		must.NoError(t, err)

		theirRow, err := store.q.GetGrant(t.Context(), env.reader(), grantsdb.GetGrantParams{ID: theirs.ID, Scope: testScope})
		must.NoError(t, err)

		// Past the store, on the generated statement: studio_2's row now holds
		// studio_1's sealed access token, which is what somebody with write
		// access to the table and none to the key could do.
		err = env.inTx(t, func(tx database.Tx) error {
			_, moveErr := store.q.RefreshGrant(t.Context(), tx, grantsdb.RefreshGrantParams{
				AccessToken:         mineRow.AccessToken,
				RefreshToken:        theirRow.RefreshToken,
				ID:                  theirs.ID,
				Scope:               testScope,
				ExpectedAccessToken: theirRow.AccessToken,
			})

			return moveErr
		})
		must.NoError(t, err)

		_, err = store.Get(t.Context(), env.reader(), testScope, "studio_2", "google")
		test.ErrorIs(t, err, encryption.ErrAuthenticationFailed)
	})

	t.Run("a consent with no expiry stores none", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// A provider that names no expiry, and issues no refresh token.
		consent := newConsent("studio_1", "google")
		consent.Tokens = Tokens{AccessToken: "a"}

		grant := env.mustPut(t, store, testScope, consent)
		test.Nil(t, grant.AccessTokenExpiresAt)
		test.EqOp(t, "", grant.RefreshToken)
	})

	t.Run("a new consent replaces a live grant", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		again := newConsent("studio_1", "google")
		again.Tokens.AccessToken = "second-access"
		again.GrantedScopes = []string{"openid"}

		second := env.mustPut(t, store, testScope, again)
		test.NotEqOp(t, first.ID, second.ID)

		got, err := store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		must.NoError(t, err)
		test.EqOp(t, second.ID, got.ID)
		test.EqOp(t, "second-access", got.AccessToken)
		test.Eq(t, []string{"openid"}, got.GrantedScopes)

		all, err := store.ListAllForSubject(t.Context(), env.reader(), testScope, "studio_1")
		must.NoError(t, err)
		test.SliceLen(t, 1, all)
	})

	t.Run("a new consent replaces a revoked grant", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))
		env.mustRevoke(t, store, testScope, first.ID, RevokedByConsumer)

		second := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		got, err := store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		must.NoError(t, err)
		test.EqOp(t, second.ID, got.ID)

		all, err := store.ListAllForSubject(t.Context(), env.reader(), testScope, "studio_1")
		must.NoError(t, err)
		test.SliceLen(t, 1, all)
	})

	t.Run("two first-time consents racing for one key converge on the later", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		later := newConsent("studio_1", "google")
		later.Tokens.AccessToken = "later-access"

		raced := make(chan error, 1)

		// The first consent holds its uncommitted row while the second one
		// starts. On Postgres and MySQL the second writer's upsert waits on that
		// row and then replaces it. On SQLite it waits for the one writer
		// connection instead. The pause only lets the second writer reach its
		// wait; a second writer that has not got there yet runs after the
		// commit and the assertions still hold.
		//
		// A delete of the key followed by an insert fails this on Postgres,
		// where the second insert meets the first's row in the unique index.
		// It passes on MySQL, because there the second delete waits on the
		// first's row too. MySQL's failure was a deadlock between two deletes
		// that both ran before either insert, which this ordering cannot
		// produce.
		err := env.inTx(t, func(tx database.Tx) error {
			if _, putErr := store.Put(t.Context(), tx, testScope, newConsent("studio_1", "google")); putErr != nil {
				return putErr
			}

			go func() {
				_, putErr := env.put(t, store, testScope, later)
				raced <- putErr
			}()

			time.Sleep(200 * time.Millisecond)

			return nil
		})
		must.NoError(t, err)
		must.NoError(t, <-raced)

		got, err := store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		must.NoError(t, err)
		test.EqOp(t, "later-access", got.AccessToken)

		all, err := store.ListAllForSubject(t.Context(), env.reader(), testScope, "studio_1")
		must.NoError(t, err)
		test.SliceLen(t, 1, all)
	})

	t.Run("a consent that revives a revoked grant clears the revocation", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))
		env.mustRevoke(t, store, testScope, first.ID, RevokedByProvider)

		second := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))
		test.NotEqOp(t, first.ID, second.ID)
		test.EqOp(t, RevocationReason(""), second.RevocationReason)
		test.Nil(t, second.RevokedAt)
		test.EqOp(t, "access-studio_1-google", second.AccessToken)
		test.True(t, first.CreatedAt.Equal(second.CreatedAt))
	})

	t.Run("one subject holds one grant per provider", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		env.mustPut(t, store, testScope, newConsent("studio_1", "google"))
		env.mustPut(t, store, testScope, newConsent("studio_1", "microsoft"))

		google, err := store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		must.NoError(t, err)
		test.EqOp(t, "access-studio_1-google", google.AccessToken)

		microsoft, err := store.Get(t.Context(), env.reader(), testScope, "studio_1", "microsoft")
		must.NoError(t, err)
		test.EqOp(t, "access-studio_1-microsoft", microsoft.AccessToken)
	})

	t.Run("a grant in another scope is absent", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		_, err := store.Get(t.Context(), env.reader(), otherScope, "studio_1", "google")
		test.ErrorIs(t, err, ErrGrantNotFound)

		// And a consent in the other scope is a second grant rather than a
		// replacement of the first.
		env.mustPut(t, store, otherScope, newConsent("studio_1", "google"))

		_, err = store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		test.NoError(t, err)
	})

	t.Run("the global scope is a scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		env.mustPut(t, store, tenancy.Global(), newConsent("studio_1", "google"))

		got, err := store.Get(t.Context(), env.reader(), tenancy.Global(), "studio_1", "google")
		must.NoError(t, err)
		test.EqOp(t, "access-studio_1-google", got.AccessToken)
	})
}

func runRefreshCases(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a refresh from the current token is stored", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		later := testExpiry.Add(time.Hour)
		refreshed, err := env.refreshed(t, store, testScope, grant.ID, grant.AccessToken,
			&Tokens{AccessToken: "access-2", RefreshToken: "refresh-2", Expiry: later})
		must.NoError(t, err)

		test.EqOp(t, grant.ID, refreshed.ID)
		test.EqOp(t, "access-2", refreshed.AccessToken)
		test.EqOp(t, "refresh-2", refreshed.RefreshToken)
		must.NotNil(t, refreshed.AccessTokenExpiresAt)
		test.True(t, later.Equal(*refreshed.AccessTokenExpiresAt))
		test.NotNil(t, refreshed.LastUpdatedAt)

		got, err := store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		must.NoError(t, err)
		test.EqOp(t, "access-2", got.AccessToken)
	})

	t.Run("a refresh with no refresh token keeps the stored one", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		refreshed, err := env.refreshed(t, store, testScope, grant.ID, grant.AccessToken, &Tokens{AccessToken: "access-2"})
		must.NoError(t, err)

		test.EqOp(t, "access-2", refreshed.AccessToken)
		test.EqOp(t, "refresh-studio_1-google", refreshed.RefreshToken)
		test.Nil(t, refreshed.AccessTokenExpiresAt)
	})

	t.Run("a refresh from a stale token is refused and writes nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		_, err := env.refreshed(t, store, testScope, grant.ID, "not-the-token", &Tokens{AccessToken: "access-2"})
		test.ErrorIs(t, err, ErrStaleRefresh)

		got, err := store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		must.NoError(t, err)
		test.EqOp(t, grant.AccessToken, got.AccessToken)
	})

	t.Run("of two refreshes from one read, the second loses", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		read := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		// Both replicas read the same grant and both called the provider. A
		// provider that rotates refresh tokens has invalidated the first
		// replica's refresh token by the time the second one answers, so the
		// second's tokens are the ones that must not be stored.
		_, err := env.refreshed(t, store, testScope, read.ID, read.AccessToken,
			&Tokens{AccessToken: "winner", RefreshToken: "winner-refresh"})
		must.NoError(t, err)

		_, err = env.refreshed(t, store, testScope, read.ID, read.AccessToken,
			&Tokens{AccessToken: "loser", RefreshToken: "loser-refresh"})
		test.ErrorIs(t, err, ErrStaleRefresh)

		got, err := store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		must.NoError(t, err)
		test.EqOp(t, "winner", got.AccessToken)
		test.EqOp(t, "winner-refresh", got.RefreshToken)
	})

	t.Run("the compare-and-set holds when the read is overtaken inside the write", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		row, err := store.q.GetGrant(t.Context(), env.reader(), grantsdb.GetGrantParams{ID: grant.ID, Scope: testScope})
		must.NoError(t, err)

		// The Go-side comparison is a fast path; the predicate is the
		// guarantee. Hand the statement the ciphertext the row held before a
		// competing refresh landed, and it matches nothing.
		_, err = env.refreshed(t, store, testScope, grant.ID, grant.AccessToken, &Tokens{AccessToken: "winner"})
		must.NoError(t, err)

		params, err := store.refreshParams(t.Context(), testScope, &row, &Tokens{AccessToken: "loser"})
		must.NoError(t, err)

		err = env.inTx(t, func(tx database.Tx) error {
			count, writeErr := store.q.RefreshGrant(t.Context(), tx, params)
			test.EqOp(t, int64(0), count)

			return writeErr
		})
		must.NoError(t, err)
	})

	t.Run("a refresh of a replaced grant finds nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		old := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))
		env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		_, err := env.refreshed(t, store, testScope, old.ID, old.AccessToken, &Tokens{AccessToken: "late"})
		test.ErrorIs(t, err, ErrGrantNotFound)
	})

	t.Run("a refresh of a revoked grant finds nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))
		env.mustRevoke(t, store, testScope, grant.ID, RevokedByProvider)

		_, err := env.refreshed(t, store, testScope, grant.ID, grant.AccessToken, &Tokens{AccessToken: "late"})
		test.ErrorIs(t, err, ErrGrantNotFound)
	})

	t.Run("a refresh in another scope finds nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		_, err := env.refreshed(t, store, otherScope, grant.ID, grant.AccessToken, &Tokens{AccessToken: "late"})
		test.ErrorIs(t, err, ErrGrantNotFound)
	})
}

func runRevokeCases(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("revoke hides the grant and empties its tokens", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		revoked := env.mustRevoke(t, store, testScope, grant.ID, RevokedByConsumer)
		test.EqOp(t, grant.ID, revoked.ID)
		must.NotNil(t, revoked.RevokedAt)
		test.EqOp(t, RevokedByConsumer, revoked.RevocationReason)
		test.EqOp(t, "", revoked.AccessToken)
		test.EqOp(t, "", revoked.RefreshToken)

		_, err := store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		test.ErrorIs(t, err, ErrGrantNotFound)

		row, err := store.q.GetRevokedGrant(t.Context(), env.reader(), grantsdb.GetRevokedGrantParams{ID: grant.ID, Scope: testScope})
		must.NoError(t, err)
		test.SliceEmpty(t, row.AccessToken)
		test.SliceEmpty(t, row.RefreshToken)
	})

	t.Run("a provider-side revocation is recorded as one", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		revoked := env.mustRevoke(t, store, testScope, grant.ID, RevokedByProvider)
		test.EqOp(t, RevokedByProvider, revoked.RevocationReason)

		_, err := store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		test.ErrorIs(t, err, ErrGrantNotFound)
	})

	t.Run("revoking twice finds nothing the second time", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))
		env.mustRevoke(t, store, testScope, grant.ID, RevokedByConsumer)

		_, err := env.revoke(t, store, testScope, grant.ID, RevokedByProvider)
		test.ErrorIs(t, err, ErrGrantNotFound)
	})

	t.Run("revoking in another scope finds nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		_, err := env.revoke(t, store, otherScope, grant.ID, RevokedByConsumer)
		test.ErrorIs(t, err, ErrGrantNotFound)

		_, err = store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		test.NoError(t, err)
	})

	t.Run("an unknown reason is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		grant := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))

		_, err := env.revoke(t, store, testScope, grant.ID, RevocationReason("bored"))
		test.ErrorIs(t, err, ErrUnknownRevocationReason)
	})
}

func runSubjectCases(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("the subject list includes revoked grants and opens no token", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		google := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))
		env.mustPut(t, store, testScope, newConsent("studio_1", "microsoft"))
		env.mustPut(t, store, testScope, newConsent("studio_2", "google"))
		env.mustPut(t, store, otherScope, newConsent("studio_1", "google"))
		env.mustRevoke(t, store, testScope, google.ID, RevokedByConsumer)

		all, err := store.ListAllForSubject(t.Context(), env.reader(), testScope, "studio_1")
		must.NoError(t, err)
		must.SliceLen(t, 2, all)

		providers := map[string]*Grant{}
		for _, grant := range all {
			test.EqOp(t, "studio_1", grant.Subject)
			test.EqOp(t, testScope, grant.Scope)
			test.EqOp(t, "", grant.AccessToken)
			test.EqOp(t, "", grant.RefreshToken)

			providers[grant.Provider] = grant
		}

		must.MapContainsKey(t, providers, "google")
		test.NotNil(t, providers["google"].RevokedAt)
		must.MapContainsKey(t, providers, "microsoft")
		test.Nil(t, providers["microsoft"].RevokedAt)
	})

	t.Run("a subject with no grants lists none", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		all, err := store.ListAllForSubject(t.Context(), env.reader(), testScope, "nobody")
		must.NoError(t, err)
		test.SliceEmpty(t, all)
	})

	t.Run("erasure deletes every grant a subject holds, revoked ones included", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		google := env.mustPut(t, store, testScope, newConsent("studio_1", "google"))
		env.mustPut(t, store, testScope, newConsent("studio_1", "microsoft"))
		env.mustPut(t, store, testScope, newConsent("studio_2", "google"))
		env.mustPut(t, store, otherScope, newConsent("studio_1", "google"))
		env.mustRevoke(t, store, testScope, google.ID, RevokedByConsumer)

		deleted, err := env.erase(t, store, testScope, "studio_1")
		must.NoError(t, err)
		test.EqOp(t, int64(2), deleted)

		all, err := store.ListAllForSubject(t.Context(), env.reader(), testScope, "studio_1")
		must.NoError(t, err)
		test.SliceEmpty(t, all)

		// The neighbor in this scope, and the same subject in the other scope,
		// are untouched.
		_, err = store.Get(t.Context(), env.reader(), testScope, "studio_2", "google")
		test.NoError(t, err)

		_, err = store.Get(t.Context(), env.reader(), otherScope, "studio_1", "google")
		test.NoError(t, err)
	})

	t.Run("erasing a subject with no grants deletes nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		deleted, err := env.erase(t, store, testScope, "nobody")
		must.NoError(t, err)
		test.EqOp(t, int64(0), deleted)
	})

	t.Run("a write is the caller's transaction's", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// A consent whose companion write fails leaves no grant behind.
		err := env.inTx(t, func(tx database.Tx) error {
			if _, putErr := store.Put(t.Context(), tx, testScope, newConsent("studio_1", "google")); putErr != nil {
				return putErr
			}

			return errCompanionFailed
		})
		test.ErrorIs(t, err, errCompanionFailed)

		_, err = store.Get(t.Context(), env.reader(), testScope, "studio_1", "google")
		test.ErrorIs(t, err, ErrGrantNotFound)
	})
}

func runRefusalCases(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("refusals", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		ctx := t.Context()

		cases := map[string]struct {
			consent func(*Consent)
			want    error
		}{
			"no subject":        {consent: func(c *Consent) { c.Subject = "" }, want: ErrEmptySubject},
			"no provider":       {consent: func(c *Consent) { c.Provider = "" }, want: ErrEmptyProvider},
			"no access token":   {consent: func(c *Consent) { c.Tokens.AccessToken = "" }, want: ErrEmptyAccessToken},
			"a spaced scope":    {consent: func(c *Consent) { c.GrantedScopes = []string{"two words"} }, want: ErrInvalidGrantedScope},
			"an empty scope":    {consent: func(c *Consent) { c.GrantedScopes = []string{""} }, want: ErrInvalidGrantedScope},
			"a long subject":    {consent: func(c *Consent) { c.Subject = strings.Repeat("s", MaxSubjectLength+1) }, want: ErrValueTooLong},
			"a long provider":   {consent: func(c *Consent) { c.Provider = strings.Repeat("p", MaxProviderLength+1) }, want: ErrValueTooLong},
			"a long account id": {consent: func(c *Consent) { c.ProviderAccountID = strings.Repeat("a", MaxProviderAccountIDLength+1) }, want: ErrValueTooLong},
			"a long token":      {consent: func(c *Consent) { c.Tokens.RefreshToken = strings.Repeat("r", MaxTokenLength+1) }, want: ErrValueTooLong},
		}

		for name, tc := range cases {
			consent := newConsent("studio_1", "google")
			tc.consent(consent)

			_, err := env.put(t, store, testScope, consent)
			test.ErrorIs(t, err, tc.want, test.Sprintf("case %q", name))
		}

		_, err := env.put(t, store, testScope, nil)
		test.ErrorIs(t, err, ErrNilConsent)

		_, err = env.put(t, store, tenancy.Scope{}, newConsent("studio_1", "google"))
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = store.Put(ctx, nil, testScope, newConsent("studio_1", "google"))
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.Get(ctx, nil, testScope, "studio_1", "google")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.Get(ctx, env.reader(), testScope, "", "google")
		test.ErrorIs(t, err, ErrEmptySubject)

		_, err = store.Get(ctx, env.reader(), testScope, "studio_1", "")
		test.ErrorIs(t, err, ErrEmptyProvider)

		_, err = store.Refreshed(ctx, nil, testScope, "id", "a", &Tokens{AccessToken: "b"})
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = env.refreshed(t, store, testScope, "id", "a", nil)
		test.ErrorIs(t, err, ErrNilTokens)

		_, err = env.refreshed(t, store, testScope, "id", "a", &Tokens{})
		test.ErrorIs(t, err, ErrEmptyAccessToken)

		_, err = env.refreshed(t, store, testScope, "", "a", &Tokens{AccessToken: "b"})
		test.ErrorIs(t, err, ErrGrantNotFound)

		_, err = store.Revoke(ctx, nil, testScope, "id", RevokedByConsumer)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListAllForSubject(ctx, nil, testScope, "studio_1")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListAllForSubject(ctx, env.reader(), testScope, "")
		test.ErrorIs(t, err, ErrEmptySubject)

		_, err = store.DeleteForSubject(ctx, nil, testScope, "studio_1")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = env.erase(t, store, testScope, "")
		test.ErrorIs(t, err, ErrEmptySubject)
	})
}

// errCompanionFailed stands in for the audit entry or outbox event a consumer
// writes beside a grant, failing.
var errCompanionFailed = errors.New("the companion write failed")
