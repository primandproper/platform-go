package database

import (
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// What every method does when the database underneath it is gone.
//
// A closed client is the cheapest honest stand-in for an outage, and it reaches
// the branch each method has beside its protocol answers: a failure to read is
// not an absent row, and a failure to write is not a duplicate. Reporting one as
// the other is how a database outage turns into a login that silently did not
// take.
func TestStore_ClosedDatabase(T *testing.T) {
	T.Parallel()

	T.Run("every read and write reports rather than answering", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)

		// Close is the store's own, and it is what releases the client.
		must.NoError(t, store.Close())

		now := time.Now().UTC()

		test.Error(t, store.CreateClient(ctx, &oauth2server.Client{CreatedAt: now, ID: "x"}))
		test.Error(t, store.DeleteClient(ctx, "x"))
		test.Error(t, store.CreateAuthorizationCode(ctx, &oauth2server.AuthorizationCode{
			IssuedAt: now, ExpiresAt: now.Add(time.Minute), Hash: oauth2server.Hash("c"), ClientID: "x",
		}))
		test.Error(t, store.CreateAccessToken(ctx, &oauth2server.AccessToken{
			IssuedAt: now, ExpiresAt: now.Add(time.Minute), Hash: oauth2server.Hash("a"), ClientID: "x",
		}))
		test.Error(t, store.CreateRefreshToken(ctx, &oauth2server.RefreshToken{
			IssuedAt: now, ExpiresAt: now.Add(time.Minute), Hash: oauth2server.Hash("r"), ClientID: "x",
		}))
		test.Error(t, store.RevokeAccessToken(ctx, oauth2server.Hash("a")))
		test.Error(t, store.RevokeRefreshToken(ctx, oauth2server.Hash("r")))

		for _, read := range []func() error{
			func() error { _, err := store.GetClient(ctx, "x"); return err },
			func() error { _, err := store.GetAccessToken(ctx, oauth2server.Hash("a")); return err },
			func() error { _, err := store.GetRefreshToken(ctx, oauth2server.Hash("r")); return err },
			func() error { _, err := store.ConsumeAuthorizationCode(ctx, oauth2server.Hash("c")); return err },
			func() error { _, err := store.ConsumeRefreshToken(ctx, oauth2server.Hash("r")); return err },
			func() error { _, err := store.RevokeFamily(ctx, "fam"); return err },
			func() error { _, err := store.Sweep(ctx, now); return err },
		} {
			err := read()
			must.Error(t, err)

			// Not ErrNotFound. A caller that read a broken database as an empty
			// one would answer /token with invalid_grant and send a user back
			// through a login that will fail the same way.
			test.False(t, stderrors.Is(err, oauth2server.ErrNotFound))
		}
	})
}

// The text columns are the one place a row can be wrong in a way no constraint
// catches, because SQL has nothing to say about whether a TEXT column holds the
// JSON this package put there.
func TestStore_UndecodableColumns(T *testing.T) {
	T.Parallel()

	T.Run("a registration whose list columns are not lists is an error", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)

		_, err := store.db.Writer().ExecContext(ctx,
			"INSERT INTO oauth2_clients (id, secret_hash, name, redirect_uris, grant_types, "+
				"response_types, scopes, token_endpoint_auth_method, created_at, expires_at) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			"corrupt", "", "", "not json", "[]", "[]", "[]", "none", time.Now().UTC(), nil)
		must.NoError(t, err)

		// Reported rather than returned as a registration with no redirect
		// URIs, which would be a client this server refuses every request from
		// for a reason nobody could find.
		got, err := store.GetClient(ctx, "corrupt")
		test.Error(t, err)
		test.Nil(t, got)
	})

	T.Run("a token whose claims column is not an object is an error", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)
		now := time.Now().UTC()

		_, err := store.db.Writer().ExecContext(ctx,
			"INSERT INTO oauth2_access_tokens (hash, client_id, family_id, subject_id, "+
				"subject_claims, scopes, audience, issued_at, expires_at, revoked_at) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			"corrupt", "x", "fam", "user_1", "[1,2,3]", "[]", "[]", now, now.Add(time.Hour), nil)
		must.NoError(t, err)

		got, err := store.GetAccessToken(ctx, "corrupt")
		test.Error(t, err)
		test.Nil(t, got)
	})

	T.Run("a refresh token whose resources column is not a list is an error", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)
		now := time.Now().UTC()

		_, err := store.db.Writer().ExecContext(ctx,
			"INSERT INTO oauth2_refresh_tokens (hash, client_id, family_id, subject_id, "+
				"subject_claims, scopes, audience, resources, issued_at, expires_at, "+
				"redeemed_at, revoked_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			"corrupt", "x", "fam", "user_1", "{}", "[]", "[]", "nope", now, now.Add(time.Hour), nil, nil)
		must.NoError(t, err)

		got, err := store.GetRefreshToken(ctx, "corrupt")
		test.Error(t, err)
		test.Nil(t, got)

		// And the consume path reads the same row through the same scan, inside
		// its transaction.
		consumed, err := store.ConsumeRefreshToken(ctx, "corrupt")
		test.Error(t, err)
		test.Nil(t, consumed)
	})

	T.Run("a code whose scopes column is not a list is an error", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)
		now := time.Now().UTC()

		_, err := store.db.Writer().ExecContext(ctx,
			"INSERT INTO oauth2_authorization_codes (hash, client_id, family_id, redirect_uri, "+
				"code_challenge, nonce, subject_id, subject_claims, scopes, resources, "+
				"issued_at, expires_at, redeemed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			"corrupt", "x", "fam", "", "", "", "user_1", "{}", "nope", "[]", now, now.Add(time.Hour), nil)
		must.NoError(t, err)

		got, err := store.ConsumeAuthorizationCode(ctx, "corrupt")
		test.Error(t, err)
		test.Nil(t, got)
	})
}

// The third case redemptionOutcome has to answer, which SQL is not supposed to
// produce.
func TestRedemptionOutcome(T *testing.T) {
	T.Parallel()

	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

	T.Run("a redeemed record is a replay, and comes back with its record", func(t *testing.T) {
		t.Parallel()

		record := &oauth2server.RefreshToken{FamilyID: "fam"}

		got, err := redemptionOutcome(record, now.Add(-time.Minute), now, now.Add(time.Hour))
		test.ErrorIs(t, err, oauth2server.ErrAlreadyRedeemed)

		// The caller needs it: revoking what the replayed credential issued is
		// not possible without knowing which family it belongs to.
		must.NotNil(t, got)
		test.EqOp(t, "fam", got.FamilyID)
	})

	T.Run("an expired record is expired, and comes back with nothing", func(t *testing.T) {
		t.Parallel()

		got, err := redemptionOutcome(&oauth2server.RefreshToken{}, time.Time{}, now, now.Add(-time.Hour))
		test.ErrorIs(t, err, oauth2server.ErrExpired)

		// Nothing to revoke, and handing the record back would only invite the
		// caller to act on it.
		test.Nil(t, got)
	})

	T.Run("a record that is neither is refused rather than retried", func(t *testing.T) {
		t.Parallel()

		// Unredeemed, unexpired, and the guarded UPDATE matched nothing anyway.
		// The only way there is another transaction changing the row between
		// the two statements, which the transaction is supposed to prevent — so
		// it is reported as unusable rather than quietly retried.
		got, err := redemptionOutcome(&oauth2server.RefreshToken{}, time.Time{}, now, now.Add(time.Hour))
		test.ErrorIs(t, err, oauth2server.ErrExpired)
		test.Nil(t, got)
	})
}
