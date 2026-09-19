package refreshtokens

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"
	"github.com/primandproper/primitives-go/v2/random"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(nil, newTestClient(t))
		test.Nil(t, store)
		test.ErrorIs(t, err, ErrNilConfig)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(&Config{}, nil)
		test.Nil(t, store)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("refuses a prefix the schema cannot render", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(&Config{TablePrefix: "ddb_"}, newTestClient(t))
		test.Nil(t, store)
		test.ErrorIs(t, err, ddl.ErrPrefixTrailingSeparator)
	})

	// The prefix reaches the statements rather than only the DDL, which is the
	// half a store that rendered its own table name would get wrong.
	T.Run("addresses the namespaced table", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		createTable(t, client, dialect.SQLite, "ddb")

		store, err := NewSQLStore(&Config{TablePrefix: "ddb"}, client, WithClock(newFakeClock()))
		must.NoError(t, err)

		issuance, err := issueFor(t, store, testScope(), &signin.RefreshTokenRequest{
			TTL:             testTTL,
			FamilyID:        testFamilyID,
			SubjectID:       testSubject,
			ActiveAccountID: testAccount,
		})
		must.NoError(t, err)
		must.NotNil(t, issuance)

		test.EqOp(t, 1, rowsIn(t, client, "ddb_signin_refresh_tokens"))
		test.EqOp(t, 0, rowsIn(t, client, "signin_refresh_tokens"))
	})
}

func TestSQLStore_Issue(T *testing.T) {
	T.Parallel()

	// The mint answers with the row it wrote and the secret, and the secret is
	// the only place the credential exists outside the holder's hands.
	T.Run("returns the row and the secret", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		issuance := issue(t, store)

		test.NotEqOp(t, "", issuance.Secret)
		must.NotNil(t, issuance.Token)

		token := issuance.Token
		test.EqOp(t, testScope(), token.Scope)
		test.EqOp(t, testFamilyID, token.FamilyID)
		test.EqOp(t, testSubject, token.SubjectID)
		test.EqOp(t, testAccount, token.ActiveAccountID)
		test.False(t, token.Administrative)
		test.EqOp(t, c.Now().UTC(), token.IssuedAt)
		test.EqOp(t, c.Now().UTC().Add(testTTL), token.ExpiresAt)
		test.Nil(t, token.RedeemedAt)
		test.Nil(t, token.RevokedAt)

		// The purge deadline is past the expiry, which is what keeps a replayed
		// token distinguishable from one nobody ever minted.
		test.True(t, token.PurgeAfter.After(token.ExpiresAt))
		test.EqOp(t, token.ExpiresAt.Add(DefaultRetention), token.PurgeAfter)
	})

	// The whole point of the hash column: a dump of this table contains nothing
	// that can be presented.
	T.Run("stores a digest and never the token", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issuance := issue(t, store)

		var hash string
		must.NoError(t, store.db.Writer().QueryRowContext(t.Context(),
			"SELECT hash FROM signin_refresh_tokens").Scan(&hash))

		test.NotEqOp(t, issuance.Secret, hash)
		test.EqOp(t, store.Digest(issuance.Secret), hash)
		test.False(t, strings.Contains(hash, issuance.Secret))
	})

	T.Run("refuses a mint that names nothing", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		for name, request := range map[string]*signin.RefreshTokenRequest{
			"no request": nil,
			"no family": {
				TTL: testTTL, SubjectID: testSubject,
			},
			"no subject": {
				TTL: testTTL, FamilyID: testFamilyID,
			},
			"no lifetime": {
				FamilyID: testFamilyID, SubjectID: testSubject,
			},
		} {
			issuance, err := issueFor(t, store, testScope(), request)
			test.Nil(t, issuance, test.Sprintf("case %q", name))
			test.Error(t, err, test.Sprintf("case %q", name))
		}
	})

	// An account is genuinely optional: a user who belongs to none signs in and
	// gets a token against nothing rather than being refused.
	T.Run("accepts a mint with no account on it", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issuance, err := issueFor(t, store, testScope(), &signin.RefreshTokenRequest{
			TTL:       testTTL,
			FamilyID:  testFamilyID,
			SubjectID: testSubject,
		})
		must.NoError(t, err)
		test.EqOp(t, "", issuance.Token.ActiveAccountID)
	})
}

func TestSQLStore_Redeem(T *testing.T) {
	T.Parallel()

	// The successful exchange, and what the successor inherits from it — the
	// account and the door included, since both decide what the successor is.
	T.Run("spends a live token and answers with what the successor inherits", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		issuance, err := issueFor(t, store, testScope(), &signin.RefreshTokenRequest{
			TTL:             testTTL,
			FamilyID:        testFamilyID,
			SubjectID:       testSubject,
			ActiveAccountID: testAccount,
			Administrative:  true,
		})
		must.NoError(t, err)

		spent, err := redeem(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)
		must.NotNil(t, spent)

		test.EqOp(t, testFamilyID, spent.FamilyID)
		test.EqOp(t, testSubject, spent.SubjectID)
		test.EqOp(t, testAccount, spent.ActiveAccountID)
		test.True(t, spent.Administrative)

		must.NotNil(t, spent.RedeemedAt)
		test.EqOp(t, c.Now().UTC(), *spent.RedeemedAt)
	})

	// The acceptance criterion this package exists for: two exchanges of one
	// token, and the second ends the login.
	T.Run("a second exchange reports reuse and revokes the family", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		first := issue(t, store)

		_, err := redeem(t, store, testScope(), first.Secret)
		must.NoError(t, err)

		// The successor the exchange would have minted. It is live at this
		// point, which is what makes the next assertion mean something.
		successor := issue(t, store)

		spent, err := redeem(t, store, testScope(), first.Secret)
		test.Nil(t, spent)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)

		// And the whole login is over: the successor whoever holds the other
		// copy is carrying no longer works either.
		_, err = redeem(t, store, testScope(), successor.Secret)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.False(t, platformerrors.Is(err, signin.ErrRefreshTokenReused))
	})

	// The revocation and the report are one act. A caller that saw the sentinel
	// and then failed to revoke would have detected a theft and allowed it to
	// continue, so the store does both or neither.
	T.Run("the family revocation lands in the same transaction as the refusal", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issuance := issue(t, store)

		_, err := redeem(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)

		_, err = redeem(t, store, testScope(), issuance.Secret)
		must.ErrorIs(t, err, signin.ErrRefreshTokenReused)

		// The refusal rolled its transaction back, and the revocation is still
		// there — which it would not be if the store had left it to a caller.
		var revoked int
		must.NoError(t, store.db.Writer().QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM signin_refresh_tokens WHERE revoked_at IS NOT NULL").Scan(&revoked))

		test.EqOp(t, 1, revoked)
	})

	// Every refusal that is not a replay is the same one, for the reason the
	// password door collapses its four.
	T.Run("collapses every other refusal", func(T *testing.T) {
		T.Parallel()

		T.Run("an unknown token", func(t *testing.T) {
			t.Parallel()

			store, _ := newTestStore(t)

			_, err := redeem(t, store, testScope(), "never-minted")
			test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		})

		T.Run("a token from another directory", func(t *testing.T) {
			t.Parallel()

			store, _ := newTestStore(t)

			issuance := issue(t, store)

			_, err := redeem(t, store, tenancy.Of("tenant_b"), issuance.Secret)
			test.ErrorIs(t, err, signin.ErrInvalidCredentials)

			// And it is still spendable where it belongs: the wrong directory
			// refused it rather than consuming it.
			_, err = redeem(t, store, testScope(), issuance.Secret)
			test.NoError(t, err)
		})

		T.Run("a token past its deadline", func(t *testing.T) {
			t.Parallel()

			store, c := newTestStore(t)

			issuance := issue(t, store)
			c.advance(testTTL + time.Minute)

			_, err := redeem(t, store, testScope(), issuance.Secret)
			test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		})

		// A revoked token was never spent, so reporting reuse for it would end a
		// family every time somebody signs out and their client retries.
		T.Run("a revoked token", func(t *testing.T) {
			t.Parallel()

			store, _ := newTestStore(t)

			issuance := issue(t, store)

			revoked, err := revokeFamily(t, store, testScope(), testFamilyID)
			must.NoError(t, err)
			must.EqOp(t, int64(1), revoked)

			_, err = redeem(t, store, testScope(), issuance.Secret)
			test.ErrorIs(t, err, signin.ErrInvalidCredentials)
			test.False(t, platformerrors.Is(err, signin.ErrRefreshTokenReused))
		})
	})

	T.Run("refuses a redemption that presented nothing", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := redeem(t, store, testScope(), "")
		test.ErrorIs(t, err, ErrEmptySecret)
	})
}

func TestSQLStore_RevokeFamily(T *testing.T) {
	T.Parallel()

	T.Run("ends every token one login issued", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		first := issue(t, store)
		second := issue(t, store)

		revoked, err := revokeFamily(t, store, testScope(), testFamilyID)
		must.NoError(t, err)
		test.EqOp(t, int64(2), revoked)

		for _, issuance := range []*signin.RefreshTokenIssuance{first, second} {
			_, redeemErr := redeem(t, store, testScope(), issuance.Secret)
			test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)
		}
	})

	// A second revocation reports zero rather than moving the stamp, so the
	// record still says when the login actually stopped working.
	T.Run("is idempotent and does not restamp", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		issue(t, store)

		_, err := revokeFamily(t, store, testScope(), testFamilyID)
		must.NoError(t, err)

		at := c.Now().UTC()
		c.advance(time.Hour)

		revoked, err := revokeFamily(t, store, testScope(), testFamilyID)
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)

		var stamped time.Time
		must.NoError(t, store.db.Writer().QueryRowContext(t.Context(),
			"SELECT revoked_at FROM signin_refresh_tokens").Scan(&stamped))

		test.EqOp(t, at, stamped.UTC())
	})

	T.Run("leaves another login's tokens alone", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		other, err := issueFor(t, store, testScope(), &signin.RefreshTokenRequest{
			TTL:       testTTL,
			FamilyID:  "family_02",
			SubjectID: testSubject,
		})
		must.NoError(t, err)

		issue(t, store)

		revoked, err := revokeFamily(t, store, testScope(), testFamilyID)
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		_, err = redeem(t, store, testScope(), other.Secret)
		test.NoError(t, err)
	})

	T.Run("refuses a revocation that named no login", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := revokeFamily(t, store, testScope(), "")
		test.ErrorIs(t, err, ErrEmptyFamilyID)
	})
}

func TestSQLStore_RevokeForSubject(T *testing.T) {
	T.Parallel()

	// The statement that cannot be assembled out of family revocations: a caller
	// holding a subject identifier cannot enumerate that person's logins.
	T.Run("ends every login one person holds", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		first := issue(t, store)

		second, err := issueFor(t, store, testScope(), &signin.RefreshTokenRequest{
			TTL:       testTTL,
			FamilyID:  "family_02",
			SubjectID: testSubject,
		})
		must.NoError(t, err)

		somebodyElse, err := issueFor(t, store, testScope(), &signin.RefreshTokenRequest{
			TTL:       testTTL,
			FamilyID:  "family_03",
			SubjectID: "user_02",
		})
		must.NoError(t, err)

		revoked, err := revokeForSubject(t, store, testScope(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, int64(2), revoked)

		for _, issuance := range []*signin.RefreshTokenIssuance{first, second} {
			_, redeemErr := redeem(t, store, testScope(), issuance.Secret)
			test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)
		}

		_, err = redeem(t, store, testScope(), somebodyElse.Secret)
		test.NoError(t, err)
	})

	// Confining it to the scope excludes nothing that exists, because a subject
	// id is already scope-unique — but a store that dropped the predicate would
	// reach another tenant's rows, and this is where that shows.
	T.Run("stays inside its directory", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issue(t, store)

		revoked, err := revokeForSubject(t, store, tenancy.Of("tenant_b"), testSubject)
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)
	})

	T.Run("refuses a revocation that named nobody", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := revokeForSubject(t, store, testScope(), "")
		test.ErrorIs(t, err, ErrEmptySubjectID)
	})

	T.Run("a person who never signed in is zero and no error", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		revoked, err := revokeForSubject(t, store, testScope(), "nobody")
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)
	})
}

func TestSQLStore_Sweep(T *testing.T) {
	T.Parallel()

	// The retention window is the whole reason the sweep is keyed on
	// purge_after: a spent token has to keep answering "already spent" for a
	// while, or a replay reads as a token nobody ever minted.
	T.Run("keeps a spent token past its expiry and collects it after its purge deadline", func(t *testing.T) {
		t.Parallel()

		store, c := newTestStore(t)

		issuance := issue(t, store)

		_, err := redeem(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)

		c.advance(testTTL + time.Minute)

		swept, err := store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)

		// Still detectable, which is what the window buys.
		_, err = redeem(t, store, testScope(), issuance.Secret)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)

		c.advance(DefaultRetention)

		swept, err = store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), swept)
		test.EqOp(t, 0, rowsIn(t, store.db, "signin_refresh_tokens"))
	})

	T.Run("leaves a live token alone", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		issue(t, store)

		swept, err := store.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)
	})

	// The background loop's only effect is the sweep it runs, so the way to see
	// it ran is the row it collected.
	T.Run("the background loop sweeps on every tick", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		store, c := newTestStore(t, WithSweeper(ctx, time.Minute))

		issue(t, store)
		c.advance(testTTL + DefaultRetention + time.Minute)

		// The tick is delivered synchronously, but the sweep it triggers runs on
		// the loop's goroutine; a second tick blocks until the first is drained,
		// which is what makes the first one's work observable.
		c.tick()
		c.tick()

		test.EqOp(t, 0, rowsIn(t, store.db, "signin_refresh_tokens"))
	})

	// A sweep that fails is logged and nothing else: nothing is waiting on the
	// goroutine, and a table that grows for another interval is not a sign-in
	// that misbehaves. The line is the loop's only effect on a failure, so a loop
	// that stopped logging would fail silently.
	T.Run("a failing background sweep is logged rather than returned", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		logger := newRecordingLogger()

		store, c := newTestStore(t, WithSweeper(ctx, time.Minute), WithLogger(logger))
		must.NoError(t, store.db.Close())

		c.tick()
		c.tick()

		test.Positive(t, logger.count(backgroundSweepFailure))
	})

	T.Run("starts nothing when it was not asked to", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(&Config{}, newTestClient(t), WithSweeper(nil, time.Minute), //nolint:staticcheck // the nil context is the case under test
			WithLogger(loggingnoop.NewLogger()), WithTracerProvider(tracingnoop.NewTracerProvider()))
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}

// TestSQLStore_Issue_RefusesACollision is the property the primary key carries,
// asserted through the one seam that can produce one.
//
// A second row bearing one digest would mean the generator produced the same
// token twice, and handing the second caller a credential that redeems the
// first caller's row is the one outcome worse than a failed mint.
func TestSQLStore_Issue_RefusesACollision(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t, WithGenerator(&constantGenerator{secret: "always-the-same"}))

	_, err := issueFor(t, store, testScope(), &signin.RefreshTokenRequest{
		TTL: testTTL, FamilyID: testFamilyID, SubjectID: testSubject,
	})
	must.NoError(t, err)

	issuance, err := issueFor(t, store, testScope(), &signin.RefreshTokenRequest{
		TTL: testTTL, FamilyID: testFamilyID, SubjectID: testSubject,
	})
	test.Nil(t, issuance)
	test.Error(t, err)
}

// constantGenerator hands back the same secret every time, which is the only way
// to reach a digest collision on purpose.
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
