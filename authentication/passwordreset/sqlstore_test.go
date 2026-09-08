package passwordreset

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/random"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(&Config{}, newTestClient(t))
		must.NoError(t, err)
		must.NotNil(t, store)
		must.NotNil(t, store.q)
	})

	T.Run("with a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(nil, newTestClient(t))
		test.Nil(t, store)
		test.ErrorIs(t, err, ErrNilConfig)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("with a nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(&Config{}, nil)
		test.Nil(t, store)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("with an unusable table prefix", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(&Config{TablePrefix: "trailing_"}, newTestClient(t))
		test.Nil(t, store)
		test.Error(t, err)
	})

	// A namespace is not decoration: it renders a second table, and every
	// statement the store runs has to name that one. The store carries no table
	// name of its own to assert on — the prefix reaches the generated querier
	// and nothing else — so this is asserted where it is decided, in the rows.
	T.Run("addresses the table its namespace names", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		createTable(t, client, dialect.SQLite, "ddb")

		store, err := NewSQLStore(&Config{TablePrefix: "ddb"}, client, WithClock(newFakeClock()))
		must.NoError(t, err)

		issue(t, store, time.Hour)

		test.EqOp(t, 1, rowsIn(t, client, "ddb_password_reset_tokens"))
		test.EqOp(t, 0, rowsIn(t, client, "password_reset_tokens"))
	})
}

func TestSQLStore_Issue(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		issuance, err := issueFor(t, store, testScope(), testUserID, time.Hour)
		must.NoError(t, err)

		test.NotEqOp(t, "", issuance.Secret)
		test.NotEqOp(t, "", issuance.Token.ID)
		test.EqOp(t, testUserID, issuance.Token.UserID)
		test.EqOp(t, testScope(), issuance.Token.Scope)
		test.Nil(t, issuance.Token.RedeemedAt)
		test.EqOp(t, c.Now().UTC(), issuance.Token.CreatedAt)
		test.EqOp(t, c.Now().UTC().Add(time.Hour), issuance.Token.ExpiresAt)
	})

	// The property the whole package exists for. A digest column that held the
	// token would make a database copy a password reset for every account with
	// an outstanding link, and no signature in this package can say it does not.
	T.Run("stores a digest and never the token", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issuance := issue(t, store, time.Hour)

		var stored string
		must.NoError(t, store.db.Writer().QueryRowContext(t.Context(),
			"SELECT token_digest FROM password_reset_tokens WHERE id = ?", issuance.Token.ID,
		).Scan(&stored))

		test.NotEqOp(t, issuance.Secret, stored)
		test.EqOp(t, store.Digest(issuance.Secret), stored)

		// And nowhere else in the row either: a column added later that echoed
		// the secret would pass every other assertion here.
		var count int
		must.NoError(t, store.db.Writer().QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM password_reset_tokens WHERE id LIKE ? OR scope LIKE ? OR belongs_to_user LIKE ?",
			issuance.Secret, issuance.Secret, issuance.Secret,
		).Scan(&count))
		test.EqOp(t, 0, count)
	})

	T.Run("mints a distinct secret every time", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		seen := map[string]struct{}{}
		for range 16 {
			issuance := issue(t, store, time.Hour)

			_, repeat := seen[issuance.Secret]
			test.False(t, repeat, test.Sprintf("secret %q was issued twice", issuance.Secret))

			seen[issuance.Secret] = struct{}{}
		}
	})

	// A second request does not invalidate the first link, because the user who
	// clicks "email me a link" twice usually opens the first message.
	T.Run("leaves an outstanding token spendable", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		first := issue(t, store, time.Hour)
		second := issue(t, store, time.Hour)

		_, err := consume(t, store, testScope(), first.Secret)
		test.NoError(t, err)

		_, err = consume(t, store, testScope(), second.Secret)
		test.NoError(t, err)
	})

	T.Run("with no scope", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issuance, err := issueFor(t, store, tenancy.Scope{}, testUserID, time.Hour)
		test.Nil(t, issuance)
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	T.Run("with no user", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issuance, err := issueFor(t, store, testScope(), "", time.Hour)
		test.Nil(t, issuance)
		test.ErrorIs(t, err, ErrEmptyUserID)
	})

	T.Run("with a non-positive lifetime", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		for _, ttl := range []time.Duration{0, -time.Minute} {
			issuance, err := issueFor(t, store, testScope(), testUserID, ttl)
			test.Nil(t, issuance)
			test.ErrorIs(t, err, ErrNonPositiveLifetime)
		}
	})

	T.Run("with a generator that has no randomness", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t, WithGenerator(&failingGenerator{}))

		issuance, err := issueFor(t, store, testScope(), testUserID, time.Hour)
		test.Nil(t, issuance)
		test.ErrorIs(t, err, random.ErrNoRandomness)
	})

	// The unique index is the schema's last word on a generator that has stopped
	// generating, and it is worth proving it is actually there.
	T.Run("refuses a repeated secret", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t, WithGenerator(&constantGenerator{secret: "always-the-same"}))

		_, err := issueFor(t, store, testScope(), testUserID, time.Hour)
		must.NoError(t, err)

		issuance, err := issueFor(t, store, testScope(), testUserID, time.Hour)
		test.Nil(t, issuance)
		test.Error(t, err)
	})
}

func TestSQLStore_Verify(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		token, err := verify(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)

		test.EqOp(t, issuance.Token.ID, token.ID)
		test.EqOp(t, testUserID, token.UserID)
		test.EqOp(t, testScope(), token.Scope)
		test.Nil(t, token.RedeemedAt)
		test.EqOp(t, issuance.Token.ExpiresAt, token.ExpiresAt)
		test.EqOp(t, issuance.Token.CreatedAt, token.CreatedAt)
	})

	// Verify is the page load, not the submit: opening the link twice has to
	// leave it usable, or every reset email would work exactly once and never
	// for the person who reloaded.
	T.Run("spends nothing", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		for range 3 {
			_, err := verify(t, store, testScope(), issuance.Secret)
			must.NoError(t, err)
		}

		_, err := consume(t, store, testScope(), issuance.Secret)
		test.NoError(t, err)
	})

	T.Run("with an unknown secret", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		token, err := verify(t, store, testScope(), "never-issued")
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenNotFound)
	})

	// The tenancy doctrine's one load-bearing assertion: a token is one tenant's
	// or it is nobody's.
	T.Run("with a token from another scope", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		token, err := verify(t, store, tenancy.Of("tenant_b"), issuance.Secret)
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenNotFound)

		token, err = verify(t, store, tenancy.Global(), issuance.Secret)
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenNotFound)
	})

	T.Run("with an expired token", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		c.advance(time.Hour)

		token, err := verify(t, store, testScope(), issuance.Secret)
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenExpired)
	})

	T.Run("with a redeemed token", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		_, err := consume(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)

		token, err := verify(t, store, testScope(), issuance.Secret)
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenRedeemed)
	})

	T.Run("with no scope", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		token, err := verify(t, store, tenancy.Scope{}, "anything")
		test.Nil(t, token)
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	T.Run("with no secret", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		token, err := verify(t, store, testScope(), "")
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrEmptySecret)
	})
}

func TestSQLStore_Consume(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		token, err := consume(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)

		test.EqOp(t, issuance.Token.ID, token.ID)
		test.EqOp(t, testUserID, token.UserID)
		must.NotNil(t, token.RedeemedAt)
		test.EqOp(t, c.Now().UTC(), *token.RedeemedAt)
	})

	// Single use, enforced by the store rather than by whoever called it.
	T.Run("spends a token exactly once", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		_, err := consume(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)

		token, err := consume(t, store, testScope(), issuance.Secret)
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenRedeemed)
	})

	T.Run("records the redemption in the row", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		_, err := consume(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)

		var redeemed any
		must.NoError(t, store.db.Writer().QueryRowContext(t.Context(),
			"SELECT redeemed_at FROM password_reset_tokens WHERE id = ?", issuance.Token.ID,
		).Scan(&redeemed))

		_, ok := database.CoerceTime(redeemed)
		test.True(t, ok, test.Sprintf("redeemed_at read back as %v", redeemed))
	})

	T.Run("with an expired token", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		c.advance(time.Hour + time.Second)

		token, err := consume(t, store, testScope(), issuance.Secret)
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenExpired)
	})

	// An expired token that was refused must stay unredeemed. The refusal is
	// made before the stamp rather than by unwinding one, which matters now that
	// the transaction is the caller's: a caller that swallowed the refusal and
	// committed anyway would still have committed nothing of Consume's, so the
	// next attempt says "expired" rather than "already used".
	T.Run("leaves an expired token unredeemed", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		c.advance(2 * time.Hour)

		_, err := consume(t, store, testScope(), issuance.Secret)
		test.ErrorIs(t, err, ErrTokenExpired)

		var redeemed any
		must.NoError(t, store.db.Writer().QueryRowContext(t.Context(),
			"SELECT redeemed_at FROM password_reset_tokens WHERE id = ?", issuance.Token.ID,
		).Scan(&redeemed))
		test.Nil(t, redeemed)
	})

	T.Run("at the very instant it expires", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		// The deadline is exclusive: a token whose expires_at is now is spent.
		c.advance(time.Hour)

		token, err := consume(t, store, testScope(), issuance.Secret)
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenExpired)
	})

	T.Run("with a token from another scope", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store, time.Hour)

		token, err := consume(t, store, tenancy.Of("tenant_b"), issuance.Secret)
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenNotFound)

		// And it is still spendable in the scope it belongs to.
		_, err = consume(t, store, testScope(), issuance.Secret)
		test.NoError(t, err)
	})

	T.Run("with an unknown secret", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		token, err := consume(t, store, testScope(), "never-issued")
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrTokenNotFound)
	})

	T.Run("with no scope", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		token, err := consume(t, store, tenancy.Scope{}, "anything")
		test.Nil(t, token)
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	T.Run("with no secret", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		token, err := consume(t, store, testScope(), "")
		test.Nil(t, token)
		test.ErrorIs(t, err, ErrEmptySecret)
	})
}

func TestSQLStore_RevokeForUser(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		first := issue(t, store, time.Hour)
		second := issue(t, store, time.Hour)

		revoked, err := revokeForUser(t, store, testScope(), testUserID)
		must.NoError(t, err)
		test.EqOp(t, int64(2), revoked)

		for _, issuance := range []*Issuance{first, second} {
			_, consumeErr := consume(t, store, testScope(), issuance.Secret)
			test.ErrorIs(t, consumeErr, ErrTokenNotFound)
		}
	})

	// A redeemed row survives, so "this link has already been used" outlives the
	// revocation the completed reset ran.
	T.Run("leaves redeemed tokens in place", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		spent := issue(t, store, time.Hour)
		outstanding := issue(t, store, time.Hour)

		_, err := consume(t, store, testScope(), spent.Secret)
		must.NoError(t, err)

		revoked, err := revokeForUser(t, store, testScope(), testUserID)
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		_, err = verify(t, store, testScope(), spent.Secret)
		test.ErrorIs(t, err, ErrTokenRedeemed)

		_, err = verify(t, store, testScope(), outstanding.Secret)
		test.ErrorIs(t, err, ErrTokenNotFound)
	})

	T.Run("reaches no other user", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		mine := issue(t, store, time.Hour)

		theirs, err := issueFor(t, store, testScope(), "user_02", time.Hour)
		must.NoError(t, err)

		revoked, err := revokeForUser(t, store, testScope(), testUserID)
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		_, err = verify(t, store, testScope(), mine.Secret)
		test.ErrorIs(t, err, ErrTokenNotFound)

		_, err = verify(t, store, testScope(), theirs.Secret)
		test.NoError(t, err)
	})

	T.Run("reaches no other scope", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		mine := issue(t, store, time.Hour)

		theirs, err := issueFor(t, store, tenancy.Of("tenant_b"), testUserID, time.Hour)
		must.NoError(t, err)

		revoked, err := revokeForUser(t, store, tenancy.Of("tenant_b"), testUserID)
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		_, err = verify(t, store, testScope(), mine.Secret)
		test.NoError(t, err)

		_, err = verify(t, store, tenancy.Of("tenant_b"), theirs.Secret)
		test.ErrorIs(t, err, ErrTokenNotFound)
	})

	T.Run("with nothing outstanding", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		revoked, err := revokeForUser(t, store, testScope(), testUserID)
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)
	})

	T.Run("with no scope", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		revoked, err := revokeForUser(t, store, tenancy.Scope{}, testUserID)
		test.EqOp(t, int64(0), revoked)
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	T.Run("with no user", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		revoked, err := revokeForUser(t, store, testScope(), "")
		test.EqOp(t, int64(0), revoked)
		test.ErrorIs(t, err, ErrEmptyUserID)
	})
}

func TestSQLStore_Digest(T *testing.T) {
	T.Parallel()

	T.Run("is stable and hides its input", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		digest := store.Digest("some-token")
		test.EqOp(t, digest, store.Digest("some-token"))
		test.NotEqOp(t, "some-token", digest)
		test.NotEqOp(t, digest, store.Digest("some-other-token"))
	})
}

func TestToken_Live(T *testing.T) {
	T.Parallel()

	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	redeemed := now.Add(-time.Minute)

	for name, tc := range map[string]struct {
		token *Token
		want  bool
	}{
		"unspent and unexpired":          {token: &Token{ExpiresAt: now.Add(time.Hour)}, want: true},
		"expired":                        {token: &Token{ExpiresAt: now.Add(-time.Second)}},
		"expiring at this exact instant": {token: &Token{ExpiresAt: now}},
		"redeemed":                       {token: &Token{ExpiresAt: now.Add(time.Hour), RedeemedAt: &redeemed}},
		"nil":                            {},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, tc.want, tc.token.Live(now))
		})
	}
}

// failingGenerator is a random.Generator with no source of randomness, for the
// one branch in Issue that nothing else reaches.
type failingGenerator struct{}

var _ random.Generator = (*failingGenerator)(nil)

func (g *failingGenerator) GenerateHexEncodedString(context.Context, int) (string, error) {
	return "", random.ErrNoRandomness
}

func (g *failingGenerator) GenerateBase32EncodedString(context.Context, int) (string, error) {
	return "", random.ErrNoRandomness
}

func (g *failingGenerator) GenerateBase64EncodedString(context.Context, int) (string, error) {
	return "", random.ErrNoRandomness
}

func (g *failingGenerator) GenerateRawBytes(context.Context, int) ([]byte, error) {
	return nil, random.ErrNoRandomness
}

// constantGenerator returns one secret forever, which is what a broken source of
// randomness looks like from here.
type constantGenerator struct {
	secret string
}

var _ random.Generator = (*constantGenerator)(nil)

func (g *constantGenerator) GenerateHexEncodedString(context.Context, int) (string, error) {
	return g.secret, nil
}

func (g *constantGenerator) GenerateBase32EncodedString(context.Context, int) (string, error) {
	return g.secret, nil
}

func (g *constantGenerator) GenerateBase64EncodedString(context.Context, int) (string, error) {
	return g.secret, nil
}

func (g *constantGenerator) GenerateRawBytes(context.Context, int) ([]byte, error) {
	return []byte(g.secret), nil
}
