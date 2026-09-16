package oauth2serverstore

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/authentication/oauth2server/oauth2servertest"
	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The conformance suite is the whole of what this store shares with the memory
// one, run against real SQL. What is left below is what is genuinely this
// store's own: construction, the round trip through text columns, and the fact
// that a SQLite database file survives the Store value.
func TestStore_Conformance(T *testing.T) {
	T.Parallel()

	// One client per subtest, so nothing here declares
	// WithInstanceLocalState: two Stores over one file are two handles to the
	// same rows, which is the property the whole package exists for.
	T.Run("shared database", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		oauth2servertest.Run(t, func(tb testing.TB, c clock.Clock) oauth2server.Store {
			tb.Helper()

			store, err := NewStore(&Config{}, client, WithClock(c))
			must.NoError(tb, err)

			// Closed at the end of the subtest, which the shared client
			// survives: Close stops this store's sweeper and nothing else, so
			// the second handle the cross-instance case builds still reads the
			// same rows.
			tb.Cleanup(func() { _ = store.Close() })

			return store
		})
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, newTestStore(t))
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(nil, newTestClient(t))
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, store)
	})

	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(&Config{}, nil)
		test.ErrorIs(t, err, ErrNilClient)
		test.Nil(t, store)
	})

	T.Run("rejects a prefix that would render an illegal identifier", func(t *testing.T) {
		t.Parallel()

		// The separator is supplied by database/ddl, so a prefix carrying its
		// own would render a double underscore.
		store, err := NewStore(&Config{TablePrefix: "trailing_"}, newTestClient(t))
		test.Error(t, err)
		test.Nil(t, store)
	})

	// The prefix reaches the generated querier and nothing else, so what a
	// namespace means is asserted where it is decided — in which set of tables
	// the rows land — rather than against a string field the store used to
	// hold. A store built against a prefix nobody migrated fails every query
	// while passing any naming assertion.
	T.Run("writes to the tables under the configured prefix", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		client := newTestClient(t)
		createTables(t, client, dialect.SQLite, "tenant")

		namespaced, err := NewStore(&Config{TablePrefix: "tenant"}, client)
		must.NoError(t, err)

		plain, err := NewStore(&Config{}, client)
		must.NoError(t, err)

		must.NoError(t, namespaced.CreateClient(ctx, &oauth2server.Client{
			CreatedAt: time.Now().UTC(),
			ID:        "prefixed",
		}))

		got, err := namespaced.GetClient(ctx, "prefixed")
		must.NoError(t, err)
		test.EqOp(t, "prefixed", got.ID)

		// And the plain store cannot see it, which is what a namespace is for.
		_, err = plain.GetClient(ctx, "prefixed")
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
	})
}

// The text columns are the one place this store can lose information the memory
// store cannot, so what round-trips through them is asserted here rather than
// left to the conformance suite's field-by-field comparison.
func TestStore_Encoding(T *testing.T) {
	T.Parallel()

	T.Run("an empty list is not a nil list once, and a nil list twice", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)

		// Written empty, read back nil. The distinction has no meaning to any
		// caller — no scopes is no scopes — and pinning it here is what stops a
		// later change to encodeStrings turning "[]" into a decode error.
		client := &oauth2server.Client{
			CreatedAt:    time.Now().UTC(),
			ID:           "empty_lists",
			RedirectURIs: []string{},
			Scopes:       nil,
		}

		must.NoError(t, store.CreateClient(ctx, client))

		got, err := store.GetClient(ctx, client.ID)
		must.NoError(t, err)
		test.SliceEmpty(t, got.RedirectURIs)
		test.SliceEmpty(t, got.Scopes)
	})

	T.Run("subject claims survive as strings", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)
		now := time.Now().UTC().Truncate(time.Microsecond)

		token := &oauth2server.AccessToken{
			IssuedAt:  now,
			ExpiresAt: now.Add(time.Hour),
			Hash:      oauth2server.Hash("claims"),
			ClientID:  "client",
			FamilyID:  "family",
			Subject: oauth2server.Subject{
				ID: "user_1",
				// The application-shaped half. This store must not interpret
				// it, and must not lose it.
				Claims: map[string]string{"account_id": "acct_9", "household": "h_2"},
			},
			Audience: []string{"https://api.example/"},
		}

		must.NoError(t, store.CreateAccessToken(ctx, token))

		got, err := store.GetAccessToken(ctx, token.Hash)
		must.NoError(t, err)
		test.Eq(t, token.Subject.Claims, got.Subject.Claims)
		test.Eq(t, token.Audience, got.Audience)
	})

	T.Run("a registration with no expiry stores NULL rather than the zero time", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)

		must.NoError(t, store.CreateClient(ctx, &oauth2server.Client{
			CreatedAt: time.Now().UTC(),
			ID:        "eternal",
		}))

		// Stored as the zero time instead, this row would be swept by the very
		// next sweep and read as lapsed by every GetClient in between.
		swept, err := store.Sweep(ctx)
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)

		got, err := store.GetClient(ctx, "eternal")
		must.NoError(t, err)
		test.True(t, got.ExpiresAt.IsZero())
	})
}

func TestStore_Sweep(T *testing.T) {
	T.Parallel()

	T.Run("a sweep counts every table it emptied", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)
		past := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

		must.NoError(t, store.CreateAuthorizationCode(ctx, &oauth2server.AuthorizationCode{
			IssuedAt: past, ExpiresAt: past, Hash: oauth2server.Hash("c"), ClientID: "x",
		}))
		must.NoError(t, store.CreateAccessToken(ctx, &oauth2server.AccessToken{
			IssuedAt: past, ExpiresAt: past, Hash: oauth2server.Hash("a"), ClientID: "x",
		}))
		must.NoError(t, store.CreateRefreshToken(ctx, &oauth2server.RefreshToken{
			IssuedAt: past, ExpiresAt: past, Hash: oauth2server.Hash("r"), ClientID: "x",
		}))
		must.NoError(t, store.CreateClient(ctx, &oauth2server.Client{
			CreatedAt: past, ExpiresAt: past, ID: "x",
		}))

		// Four tables, one transaction, one number. This store is the only one
		// where a partial sweep is representable, so the count is asserted
		// exactly rather than as a lower bound. newTestStore gives each subtest
		// its own SQLite file, so the wall clock reaches nobody else's rows.
		swept, err := store.Sweep(ctx)
		must.NoError(t, err)
		test.EqOp(t, int64(4), swept)
	})

	T.Run("the horizon is the injected clock and not the wall clock", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		client := newTestClient(t)

		// One row, dead by half an hour on the wall clock.
		writer, err := NewStore(&Config{}, client)
		must.NoError(t, err)

		dead := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Microsecond)
		must.NoError(t, writer.CreateAuthorizationCode(ctx, &oauth2server.AuthorizationCode{
			IssuedAt: dead, ExpiresAt: dead, Hash: oauth2server.Hash("clocked"), ClientID: "x",
		}))

		// A store an hour behind does not reach it. Sweep takes no horizon, so
		// this is the whole of the difference between the two clocks: a sweep
		// reading the wall clock would take the row.
		behind, err := NewStore(&Config{}, client, WithClock(stoppedAt(time.Now().UTC().Add(-time.Hour))))
		must.NoError(t, err)

		swept, err := behind.Sweep(ctx)
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)

		// And a store on the wall clock does, over the same table — which is
		// what says the first result was the clock and not an empty database.
		swept, err = writer.Sweep(ctx)
		must.NoError(t, err)
		test.EqOp(t, int64(1), swept)
	})
}

// A store built on the row conversions reports their failures through its own
// methods, which is what says the descriptions in rows.go reach an operator
// rather than only a unit test.
func TestStore_DecodeFailureSurfaces(T *testing.T) {
	T.Parallel()

	T.Run("names the column an operator has to go and look at", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)

		must.NoError(t, store.CreateClient(ctx, &oauth2server.Client{
			CreatedAt: time.Now().UTC(),
			ID:        "corruptible",
			Scopes:    []string{"read"},
		}))

		// Reached through the client rather than the store, because the store
		// has no statement that writes a column this way — which is the point:
		// what it protects against is a row somebody else put there.
		_, err := store.db.Writer().ExecContext(ctx,
			"UPDATE oauth2_clients SET scopes = ? WHERE id = ?", "not json", "corruptible")
		must.NoError(t, err)

		got, err := store.GetClient(ctx, "corruptible")
		must.Error(t, err)
		test.Nil(t, got)
		test.StrContains(t, err.Error(), "decoding registered scopes")
	})
}

// Close is this store's shutdown and nobody else's.
//
// The client it was built over belongs to whoever opened it, which in a service
// is the composition root and every other store in the process. What Close has
// to release is the one thing this store started for itself.
func TestStore_Close(T *testing.T) {
	T.Parallel()

	T.Run("leaves the caller's client open", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		client := newTestClient(t)

		store, err := NewStore(&Config{}, client)
		must.NoError(t, err)

		must.NoError(t, store.Close())

		// The pool still answers, which is what the audit log, the sessions
		// table and everything else sharing this handle depend on.
		var count int
		must.NoError(t, client.Writer().
			QueryRowContext(ctx, "SELECT COUNT(*) FROM oauth2_clients").Scan(&count))
		test.EqOp(t, 0, count)

		// A store built afterwards writes through it, and the closed one reads
		// what that store wrote.
		other, err := NewStore(&Config{}, client)
		must.NoError(t, err)

		must.NoError(t, other.CreateClient(ctx, &oauth2server.Client{
			CreatedAt: time.Now().UTC().Truncate(time.Microsecond), ID: "x",
		}))

		got, err := store.GetClient(ctx, "x")
		must.NoError(t, err)
		must.NotNil(t, got)
		test.EqOp(t, "x", got.ID)
	})

	// The wall clock is deliberate rather than an injected fake: inside a
	// synctest bubble clock.NewClock reads the bubble's time, so the sweeper's
	// ticker advances with time.Sleep and needs no test double.
	T.Run("stops the sweeper", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)

			store, err := NewStore(&Config{}, client, WithSweeper(t.Context(), 10*time.Second))
			must.NoError(t, err)

			must.NoError(t, store.Close())
			synctest.Wait()

			now := time.Now().UTC()
			must.NoError(t, store.CreateAuthorizationCode(t.Context(), &oauth2server.AuthorizationCode{
				IssuedAt:  now,
				ExpiresAt: now.Add(time.Minute),
				Hash:      oauth2server.Hash("abandoned"),
				ClientID:  "x",
			}))

			time.Sleep(time.Minute + 10*time.Second)
			synctest.Wait()

			// Still there, well past its deadline and past several ticks,
			// because nothing is sweeping any more. Left running it would also
			// be sweeping through a client the caller may since have closed,
			// logging a failure every interval for the rest of the process.
			test.EqOp(t, 1, codeCount(t, store))
		})
	})

	T.Run("is safe more than once, and on a store with no sweeper", func(t *testing.T) {
		t.Parallel()

		store := newTestStore(t)
		test.Nil(t, store.stopSweeper)

		must.NoError(t, store.Close())
		must.NoError(t, store.Close())

		swept, err := NewStore(&Config{}, newTestClient(t), WithSweeper(t.Context(), time.Minute))
		must.NoError(t, err)

		must.NoError(t, swept.Close())
		must.NoError(t, swept.Close())
	})
}

// codeCount counts what is actually in the authorization code table, which is
// what a sweeper changes and a read cannot see.
func codeCount(t *testing.T, store *Store) int {
	t.Helper()

	var count int
	must.NoError(t, store.db.Writer().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM oauth2_authorization_codes").Scan(&count))

	return count
}

// RevokeSubject is the store's own method rather than the interface's, so the
// conformance suite says nothing about it and everything it owes is here: which
// rows it reaches, which it leaves, and what a second call does.
func TestStore_RevokeSubject(T *testing.T) {
	T.Parallel()

	T.Run("ends every token the subject holds, across every family", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)

		// Two logins by one person, and a third person's login beside them.
		// Nothing in the schema relates the two families, which is the whole
		// reason this cannot be assembled out of RevokeFamily.
		first := seedTokens(t, store, "person_1", "family_1")
		second := seedTokens(t, store, "person_1", "family_2")
		other := seedTokens(t, store, "person_2", "family_3")

		revoked, err := revokeSubject(t, store, "person_1")
		must.NoError(t, err)
		test.EqOp(t, int64(4), revoked)

		for _, pair := range []tokenPair{first, second} {
			_, accessErr := store.GetAccessToken(ctx, pair.access)
			test.ErrorIs(t, accessErr, oauth2server.ErrExpired)

			_, refreshErr := store.GetRefreshToken(ctx, pair.refresh)
			test.ErrorIs(t, refreshErr, oauth2server.ErrExpired)
		}

		// And the other person is still signed in, which is what says the
		// predicate is the subject rather than the table.
		access, err := store.GetAccessToken(ctx, other.access)
		must.NoError(t, err)
		test.True(t, access.RevokedAt.IsZero())

		refresh, err := store.GetRefreshToken(ctx, other.refresh)
		must.NoError(t, err)
		test.True(t, refresh.RevokedAt.IsZero())
	})

	// The count is for a metric, and the guard that makes it accurate is the
	// same one the family revocation carries: `revoked_at IS NULL`.
	T.Run("counts what it revoked, and a second call revokes nothing", func(t *testing.T) {
		t.Parallel()

		store := newTestStore(t)

		seedTokens(t, store, "person", "family")

		revoked, err := revokeSubject(t, store, "person")
		must.NoError(t, err)
		test.EqOp(t, int64(2), revoked)

		revoked, err = revokeSubject(t, store, "person")
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)
	})

	// Zero rows the second time is not only a count: the record still has to say
	// when the token actually stopped working, which a revocation that moved the
	// stamp would lose.
	T.Run("leaves a revocation already recorded where it was", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

		first, err := NewStore(&Config{}, client, WithClock(stoppedAt(at)))
		must.NoError(t, err)

		pair := seedTokens(t, first, "person", "family")

		_, err = revokeSubject(t, first, "person")
		must.NoError(t, err)

		stamped := revokedAt(t, first, "oauth2_access_tokens", pair.access)
		test.StrNotEqFold(t, "", stamped)

		// An hour later, and the stamp is still the first one's.
		later, err := NewStore(&Config{}, client, WithClock(stoppedAt(at.Add(time.Hour))))
		must.NoError(t, err)

		revoked, err := revokeSubject(t, later, "person")
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)
		test.EqOp(t, stamped, revokedAt(t, later, "oauth2_access_tokens", pair.access))
	})

	// A subject revocation reaches the two token tables and nothing else. The
	// codes table carries a subject_id as well, and a code has no revoked_at to
	// stamp — the method's documentation says what that costs.
	T.Run("leaves the codes and the registrations alone", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)
		now := time.Now().UTC().Truncate(time.Microsecond)

		must.NoError(t, store.CreateAuthorizationCode(ctx, &oauth2server.AuthorizationCode{
			IssuedAt:  now,
			ExpiresAt: now.Add(time.Hour),
			Hash:      oauth2server.Hash("outstanding"),
			ClientID:  "client",
			FamilyID:  "family",
			Subject:   oauth2server.Subject{ID: "person"},
		}))
		must.NoError(t, store.CreateClient(ctx, &oauth2server.Client{CreatedAt: now, ID: "client"}))

		seedTokens(t, store, "person", "family")

		revoked, err := revokeSubject(t, store, "person")
		must.NoError(t, err)
		test.EqOp(t, int64(2), revoked)

		// The code is still redeemable, and the registration still resolves.
		test.EqOp(t, 1, codeCount(t, store))

		code, err := store.ConsumeAuthorizationCode(ctx, oauth2server.Hash("outstanding"))
		must.NoError(t, err)
		must.NotNil(t, code)

		registration, err := store.GetClient(ctx, "client")
		must.NoError(t, err)
		test.EqOp(t, "client", registration.ID)
	})

	// What taking the caller's transaction buys, and the reason this method has
	// a shape the rest of the store does not: the revocation is undone with
	// whatever ordered it. An erasure that fails after this ran leaves the
	// person signed in, rather than signed out of an account nothing in the
	// audit trail says anybody touched.
	T.Run("is undone when the caller's transaction is", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)

		pair := seedTokens(t, store, "person", "family")

		erasureFailed := platformerrors.New("the erasure this revocation was part of")

		err := store.db.WithTransaction(ctx, func(tx database.Tx) error {
			revoked, revokeErr := store.RevokeSubject(ctx, tx, "person")
			must.NoError(t, revokeErr)
			test.EqOp(t, int64(2), revoked)

			return erasureFailed
		})
		test.ErrorIs(t, err, erasureFailed)

		// Both tokens still work, because the transaction they were revoked in
		// never committed.
		access, err := store.GetAccessToken(ctx, pair.access)
		must.NoError(t, err)
		test.True(t, access.RevokedAt.IsZero())

		refresh, err := store.GetRefreshToken(ctx, pair.refresh)
		must.NoError(t, err)
		test.True(t, refresh.RevokedAt.IsZero())
	})

	T.Run("refuses an empty subject", func(t *testing.T) {
		t.Parallel()

		revoked, err := revokeSubject(t, newTestStore(t), "")
		test.ErrorIs(t, err, oauth2server.ErrEmptyIdentifier)
		test.EqOp(t, int64(0), revoked)
	})

	// The one guard the rest of this store cannot need, because this is the one
	// method with something to be handed.
	T.Run("refuses a nil transaction", func(t *testing.T) {
		t.Parallel()

		revoked, err := newTestStore(t).RevokeSubject(t.Context(), nil, "person")
		test.ErrorIs(t, err, ErrNilTransaction)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.EqOp(t, int64(0), revoked)
	})
}

// revokeSubject runs the subject revocation in a transaction of its own, which
// is what an operator with nothing to join does. Every other revocation in this
// store opens one internally; this one is handed the caller's, so the wrapping
// is the test's to do — see Store.RevokeSubject.
func revokeSubject(t *testing.T, store *Store, subjectID string) (int64, error) {
	t.Helper()

	var revoked int64

	err := store.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		var revokeErr error

		revoked, revokeErr = store.RevokeSubject(t.Context(), tx, subjectID)

		return revokeErr
	})

	return revoked, err
}

// tokenPair is the two digests one seeded login is addressed by.
type tokenPair struct {
	access  string
	refresh string
}

// seedTokens writes one live access token and one live refresh token for a
// subject, under the named family.
func seedTokens(t *testing.T, store *Store, subject, family string) tokenPair {
	t.Helper()

	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)

	pair := tokenPair{
		access:  oauth2server.Hash("access_" + subject + "_" + family),
		refresh: oauth2server.Hash("refresh_" + subject + "_" + family),
	}

	must.NoError(t, store.CreateAccessToken(ctx, &oauth2server.AccessToken{
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Hour),
		Hash:      pair.access,
		ClientID:  "client",
		FamilyID:  family,
		Subject:   oauth2server.Subject{ID: subject},
	}))

	must.NoError(t, store.CreateRefreshToken(ctx, &oauth2server.RefreshToken{
		IssuedAt:  now,
		ExpiresAt: now.Add(24 * time.Hour),
		Hash:      pair.refresh,
		ClientID:  "client",
		FamilyID:  family,
		Subject:   oauth2server.Subject{ID: subject},
	}))

	return pair
}

// revokedAt reads a token's revocation stamp as the column actually holds it.
//
// Read through the client rather than through the store, because every read
// this store exposes refuses a revoked token — which is the behavior that makes
// the stamp itself unreachable from above, and the reason a test about when a
// revocation was recorded has to go to the row.
func revokedAt(t *testing.T, store *Store, table, hash string) string {
	t.Helper()

	var stamped *string
	must.NoError(t, store.db.Writer().
		QueryRowContext(t.Context(), "SELECT revoked_at FROM "+table+" WHERE hash = ?", hash).
		Scan(&stamped))

	if stamped == nil {
		return ""
	}

	return *stamped
}
