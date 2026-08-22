package database

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/authentication/oauth2server/oauth2servertest"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

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

		oauth2servertest.Run(t, func(tb testing.TB) oauth2server.Store {
			tb.Helper()

			store, err := NewStore(&Config{}, client)
			must.NoError(tb, err)

			// Deliberately not closed: Close releases the database client, and
			// the client here is shared by every subtest and by the second
			// handle the cross-instance case builds.
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

	T.Run("names the tables under the configured prefix", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		createTables(t, client, dialect.SQLite, "tenant")

		store, err := NewStore(&Config{TablePrefix: "tenant"}, client)
		must.NoError(t, err)

		test.EqOp(t, "tenant_oauth2_clients", store.clients)
		test.EqOp(t, "tenant_oauth2_authorization_codes", store.codes)
		test.EqOp(t, "tenant_oauth2_access_tokens", store.access)
		test.EqOp(t, "tenant_oauth2_refresh_tokens", store.refresh)

		// The prefixed tables are real, not just named: a store built against a
		// prefix nobody migrated would pass every naming assertion and fail
		// every query.
		must.NoError(t, store.CreateClient(t.Context(), &oauth2server.Client{
			CreatedAt: time.Now().UTC(),
			ID:        "prefixed",
		}))
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
		swept, err := store.Sweep(ctx, time.Now().UTC())
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
		// exactly rather than as a lower bound.
		swept, err := store.Sweep(ctx, time.Now().UTC())
		must.NoError(t, err)
		test.EqOp(t, int64(4), swept)
	})
}
