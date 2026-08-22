package oauth2servertest

import (
	stderrors "errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	// past and future are how far from now the suite writes a deadline it
	// wants to be over or not over.
	//
	// A minute in each direction, which is absurd for an authorization code and
	// exactly the point: the database store evaluates these against its
	// server's clock rather than the test process's, and no skew worth having a
	// store at all lands inside a minute.
	past   = -time.Minute
	future = time.Minute

	// longPast is the deadline the sweep cases write, and sweepHorizon is the
	// instant they sweep at — so a sweep reaches only records this suite wrote
	// for it and never the merely-expired ones the cases above depend on.
	//
	// That separation is necessary rather than tidy. One database serves every
	// subtest at once, and Sweep has no scope: a sweep at "now" would delete
	// another parallel subtest's expired code out from under the assertion that
	// consuming it reports ErrExpired, which would then report ErrNotFound
	// instead — a failure with nothing wrong behind it.
	longPast     = -2 * time.Hour
	sweepHorizon = -time.Hour

	// contenders is how many goroutines race to consume one credential in the
	// cases that prove exactly one of them wins.
	contenders = 8
)

// Factory builds one Store for one subtest. It must hand back a usable
// instance and register whatever teardown that instance needs on tb — the
// suite never closes what a factory returns.
//
// A backend whose state outlives the Store value needs no cleaning between
// subtests: every identifier the suite writes carries a unique suffix, so one
// database serves every subtest, every parallel run, and every rerun.
type Factory func(tb testing.TB) oauth2server.Store

// Option declares where an implementation stops honoring the full Store
// contract. Each one removes cases, so an implementation that declares nothing
// is held to all of it.
type Option func(*deviations)

type deviations struct {
	instanceLocalState bool
}

// WithInstanceLocalState declares that this Store keeps its records inside the
// Store value rather than somewhere a second instance could reach them.
//
// The memory store is this one, and it is not a testing detail: it is the
// reason a fleet running that store fails logins, because the authorization
// code is written by the replica that served /authorize and read by whichever
// replica serves /token. Declaring it here is the implementation saying out
// loud what its doc says in prose.
func WithInstanceLocalState() Option {
	return func(d *deviations) { d.instanceLocalState = true }
}

// Run asserts every behavior an oauth2server.Store owes its callers against
// the implementation newStore builds, as one parallel subtest per behavior.
func Run(t *testing.T, newStore Factory, opts ...Option) {
	t.Helper()

	var d deviations
	for _, opt := range opts {
		if opt != nil {
			opt(&d)
		}
	}

	runClientCases(t, newStore, d)
	runAuthorizationCodeCases(t, newStore)
	runAccessTokenCases(t, newStore)
	runRefreshTokenCases(t, newStore)
	runFamilyCases(t, newStore)
	runSweepCases(t, newStore)
}

//nolint:gocognit // one subtest per behavior; splitting further would separate a case from its name.
func runClientCases(t *testing.T, newStore Factory, d deviations) {
	t.Helper()

	t.Run("a registered client round-trips every field", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		client := newClient(future)

		must.NoError(t, store.CreateClient(ctx, client))

		got, err := store.GetClient(ctx, client.ID)
		must.NoError(t, err)
		must.NotNil(t, got)

		test.EqOp(t, client.ID, got.ID)
		test.EqOp(t, client.SecretHash, got.SecretHash)
		test.EqOp(t, client.Name, got.Name)
		test.EqOp(t, client.TokenEndpointAuthMethod, got.TokenEndpointAuthMethod)
		test.Eq(t, client.RedirectURIs, got.RedirectURIs)
		test.Eq(t, client.GrantTypes, got.GrantTypes)
		test.Eq(t, client.ResponseTypes, got.ResponseTypes)
		test.Eq(t, client.Scopes, got.Scopes)
	})

	t.Run("a second registration under one identifier is refused", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		client := newClient(future)

		must.NoError(t, store.CreateClient(ctx, client))

		// Not an overwrite. Registration is open to anonymous callers, so an
		// overwrite would let one of them take over another's client by
		// guessing an identifier.
		second := newClient(future)
		second.ID = client.ID
		second.Name = "impostor"

		test.ErrorIs(t, store.CreateClient(ctx, second), oauth2server.ErrClientExists)

		got, err := store.GetClient(ctx, client.ID)
		must.NoError(t, err)
		test.EqOp(t, client.Name, got.Name)
	})

	t.Run("an absent client is ErrNotFound", func(t *testing.T) {
		t.Parallel()

		got, err := newStore(t).GetClient(t.Context(), unique("nobody"))
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
		test.Nil(t, got)
	})

	t.Run("a lapsed registration is ErrExpired", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		client := newClient(past)

		must.NoError(t, store.CreateClient(ctx, client))

		got, err := store.GetClient(ctx, client.ID)
		test.ErrorIs(t, err, oauth2server.ErrExpired)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
		test.Nil(t, got)
	})

	t.Run("a registration with no expiry does not lapse", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		client := newClient(future)
		// The zero time is "never", not "lapsed in year one" — the distinction
		// a naive `now.After(expiresAt)` gets backwards.
		client.ExpiresAt = time.Time{}

		must.NoError(t, store.CreateClient(ctx, client))

		got, err := store.GetClient(ctx, client.ID)
		must.NoError(t, err)
		must.NotNil(t, got)
		test.True(t, got.ExpiresAt.IsZero())
	})

	t.Run("deleting a client removes it, and deleting an absent one is not an error", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		client := newClient(future)

		must.NoError(t, store.CreateClient(ctx, client))
		must.NoError(t, store.DeleteClient(ctx, client.ID))

		_, err := store.GetClient(ctx, client.ID)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)

		// The caller wanted it gone and it is gone.
		test.NoError(t, store.DeleteClient(ctx, client.ID))
		test.NoError(t, store.DeleteClient(ctx, unique("never_existed")))
	})

	t.Run("what a read hands back cannot be written back through", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		client := newClient(future)

		must.NoError(t, store.CreateClient(ctx, client))

		got, err := store.GetClient(ctx, client.ID)
		must.NoError(t, err)

		// A map-backed store that hands out its own pointer lets a caller add
		// a redirect URI to a registration by editing the value it read.
		got.RedirectURIs = append(got.RedirectURIs, "https://attacker.example/cb")
		got.SecretHash = ""

		reread, err := store.GetClient(ctx, client.ID)
		must.NoError(t, err)
		test.Eq(t, client.RedirectURIs, reread.RedirectURIs)
		test.EqOp(t, client.SecretHash, reread.SecretHash)
	})

	t.Run("a registration is visible to a second handle on the same store", func(t *testing.T) {
		t.Parallel()

		if d.instanceLocalState {
			t.Skip("declared WithInstanceLocalState: records do not outlive the Store value")
		}

		ctx := t.Context()
		writer, reader := newStore(t), newStore(t)
		client := newClient(future)

		must.NoError(t, writer.CreateClient(ctx, client))

		// This is the property the whole package exists for. A registration
		// written by the replica serving /register has to be readable by the
		// replica serving /token, or dynamic registration works only when the
		// load balancer happens not to spread the flow.
		got, err := reader.GetClient(ctx, client.ID)
		must.NoError(t, err)
		must.NotNil(t, got)
		test.EqOp(t, client.ID, got.ID)
	})

	t.Run("an empty identifier is refused rather than matching a row", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)

		_, err := store.GetClient(ctx, "")
		test.ErrorIs(t, err, oauth2server.ErrEmptyIdentifier)

		test.ErrorIs(t, store.DeleteClient(ctx, ""), oauth2server.ErrEmptyIdentifier)
		test.ErrorIs(t, store.CreateClient(ctx, nil), oauth2server.ErrNilRecord)

		empty := newClient(future)
		empty.ID = ""
		test.ErrorIs(t, store.CreateClient(ctx, empty), oauth2server.ErrEmptyIdentifier)
	})
}

//nolint:gocognit // one subtest per behavior; splitting further would separate a case from its name.
func runAuthorizationCodeCases(t *testing.T, newStore Factory) {
	t.Helper()

	t.Run("an issued code round-trips through a redemption", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		code := newCode(future)

		must.NoError(t, store.CreateAuthorizationCode(ctx, code))

		got, err := store.ConsumeAuthorizationCode(ctx, code.Hash)
		must.NoError(t, err)
		must.NotNil(t, got)

		test.EqOp(t, code.ClientID, got.ClientID)
		test.EqOp(t, code.FamilyID, got.FamilyID)
		test.EqOp(t, code.RedirectURI, got.RedirectURI)
		test.EqOp(t, code.CodeChallenge, got.CodeChallenge)
		test.EqOp(t, code.Nonce, got.Nonce)
		test.EqOp(t, code.Subject.ID, got.Subject.ID)
		test.Eq(t, code.Subject.Claims, got.Subject.Claims)
		test.Eq(t, code.Scopes, got.Scopes)
		test.Eq(t, code.Resources, got.Resources)
	})

	t.Run("a second redemption is a replay, and says which code was replayed", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		code := newCode(future)

		must.NoError(t, store.CreateAuthorizationCode(ctx, code))

		_, err := store.ConsumeAuthorizationCode(ctx, code.Hash)
		must.NoError(t, err)

		replayed, err := store.ConsumeAuthorizationCode(ctx, code.Hash)
		test.ErrorIs(t, err, oauth2server.ErrAlreadyRedeemed)

		// The record comes back with the error. Without it the caller cannot
		// find what this code issued, and revoking that is the entire response
		// to a replay.
		must.NotNil(t, replayed)
		test.EqOp(t, code.ClientID, replayed.ClientID)
		test.EqOp(t, code.Subject.ID, replayed.Subject.ID)

		// And the family with it, which is the field the revocation is by. A
		// store that dropped it would satisfy every other case here and leave
		// the replay detectable and unanswerable.
		test.EqOp(t, code.FamilyID, replayed.FamilyID)
	})

	t.Run("an absent code is ErrNotFound", func(t *testing.T) {
		t.Parallel()

		got, err := newStore(t).ConsumeAuthorizationCode(t.Context(), oauth2server.Hash(unique("nothing")))
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
		test.Nil(t, got)
	})

	t.Run("an expired code is refused and is not thereby marked redeemed", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		code := newCode(past)

		must.NoError(t, store.CreateAuthorizationCode(ctx, code))

		got, err := store.ConsumeAuthorizationCode(ctx, code.Hash)
		test.ErrorIs(t, err, oauth2server.ErrExpired)
		test.Nil(t, got)

		// The second half is the one a guarded UPDATE gets right and a
		// read-check-write does not: a failed consume must not have written
		// anything, or the next attempt reports a replay that never happened
		// and revokes a family over it.
		_, err = store.ConsumeAuthorizationCode(ctx, code.Hash)
		test.ErrorIs(t, err, oauth2server.ErrExpired)
		test.False(t, isReplay(err))
	})

	t.Run("concurrent redemptions of one code produce exactly one success", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		code := newCode(future)

		must.NoError(t, store.CreateAuthorizationCode(ctx, code))

		var (
			wg        sync.WaitGroup
			succeeded atomic.Int64
			replayed  atomic.Int64
			start     = make(chan struct{})
		)

		errs := make([]error, contenders)

		for i := range contenders {
			wg.Go(func() {
				<-start

				redeemed, err := store.ConsumeAuthorizationCode(ctx, code.Hash)
				switch {
				case err == nil:
					succeeded.Add(1)
					errs[i] = nil

					if redeemed == nil {
						errs[i] = oauth2server.ErrNilRecord
					}
				case isReplay(err):
					replayed.Add(1)
				default:
					errs[i] = err
				}
			})
		}

		close(start)
		wg.Wait()

		for _, err := range errs {
			must.NoError(t, err)
		}

		// One token pair per code. A store that reads and then writes lets two
		// of these through, and the credential that was supposed to be
		// single-use was used twice.
		test.EqOp(t, int64(1), succeeded.Load())
		test.EqOp(t, int64(contenders-1), replayed.Load())
	})

	t.Run("a second code under one hash is refused", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		code := newCode(future)

		must.NoError(t, store.CreateAuthorizationCode(ctx, code))

		// Not an overwrite, and the difference is a redemption. A code arriving
		// at a hash that already holds one is either a collision in the
		// generator or a caller reusing a value; overwriting would reset
		// redeemed_at, which is the only record that the first one was spent.
		second := newCode(future)
		second.Hash = code.Hash
		second.ClientID = "impostor"

		test.ErrorIs(t, store.CreateAuthorizationCode(ctx, second), oauth2server.ErrRecordExists)

		got, err := store.ConsumeAuthorizationCode(ctx, code.Hash)
		must.NoError(t, err)
		must.NotNil(t, got)
		test.EqOp(t, code.ClientID, got.ClientID)
	})

	t.Run("an empty hash is refused rather than matching a row", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)

		_, err := store.ConsumeAuthorizationCode(ctx, "")
		test.ErrorIs(t, err, oauth2server.ErrEmptyIdentifier)

		test.ErrorIs(t, store.CreateAuthorizationCode(ctx, nil), oauth2server.ErrNilRecord)

		// A record whose hash is empty is refused on the way in as well.
		// Storing it would put a row under a key every empty-string lookup
		// matches, which is the one credential nobody has to steal.
		empty := newCode(future)
		empty.Hash = ""
		test.ErrorIs(t, store.CreateAuthorizationCode(ctx, empty), oauth2server.ErrEmptyIdentifier)
	})
}

func runAccessTokenCases(t *testing.T, newStore Factory) {
	t.Helper()

	t.Run("an issued access token round-trips", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newAccessToken(future, unique("family"))

		must.NoError(t, store.CreateAccessToken(ctx, token))

		got, err := store.GetAccessToken(ctx, token.Hash)
		must.NoError(t, err)
		must.NotNil(t, got)

		test.EqOp(t, token.ClientID, got.ClientID)
		test.EqOp(t, token.FamilyID, got.FamilyID)
		test.EqOp(t, token.Subject.ID, got.Subject.ID)
		test.Eq(t, token.Subject.Claims, got.Subject.Claims)
		test.Eq(t, token.Scopes, got.Scopes)

		// The audience is what stops a token minted for one resource server
		// being replayed at another, so it has to survive the round trip.
		test.Eq(t, token.Audience, got.Audience)
	})

	t.Run("an absent access token is ErrNotFound", func(t *testing.T) {
		t.Parallel()

		got, err := newStore(t).GetAccessToken(t.Context(), oauth2server.Hash(unique("nothing")))
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
		test.Nil(t, got)
	})

	t.Run("an expired access token is ErrExpired", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newAccessToken(past, unique("family"))

		must.NoError(t, store.CreateAccessToken(ctx, token))

		got, err := store.GetAccessToken(ctx, token.Hash)
		test.ErrorIs(t, err, oauth2server.ErrExpired)
		test.Nil(t, got)
	})

	t.Run("a revoked access token stops reading as usable", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newAccessToken(future, unique("family"))

		must.NoError(t, store.CreateAccessToken(ctx, token))
		must.NoError(t, store.RevokeAccessToken(ctx, token.Hash))

		got, err := store.GetAccessToken(ctx, token.Hash)
		test.ErrorIs(t, err, oauth2server.ErrExpired)
		test.Nil(t, got)
	})

	t.Run("revoking is idempotent and never reports what does not exist", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newAccessToken(future, unique("family"))

		must.NoError(t, store.CreateAccessToken(ctx, token))

		// RFC 7009 §2.2 has /revoke answer 200 whatever it was given, so a
		// store that distinguished "revoked" from "never existed" would be
		// inviting that endpoint to leak which tokens exist.
		test.NoError(t, store.RevokeAccessToken(ctx, token.Hash))
		test.NoError(t, store.RevokeAccessToken(ctx, token.Hash))
		test.NoError(t, store.RevokeAccessToken(ctx, oauth2server.Hash(unique("nothing"))))
	})

	t.Run("a second access token under one hash is refused", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newAccessToken(future, unique("family"))

		must.NoError(t, store.CreateAccessToken(ctx, token))

		// An overwrite here would move a live token into another family, and
		// the family is what a reuse detection revokes — so the token an
		// attacker holds would survive the revocation of the session it
		// belonged to.
		second := newAccessToken(future, unique("family"))
		second.Hash = token.Hash

		test.ErrorIs(t, store.CreateAccessToken(ctx, second), oauth2server.ErrRecordExists)

		got, err := store.GetAccessToken(ctx, token.Hash)
		must.NoError(t, err)
		test.EqOp(t, token.FamilyID, got.FamilyID)
	})

	t.Run("an empty hash is refused rather than matching a row", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)

		_, err := store.GetAccessToken(ctx, "")
		test.ErrorIs(t, err, oauth2server.ErrEmptyIdentifier)
		test.ErrorIs(t, store.RevokeAccessToken(ctx, ""), oauth2server.ErrEmptyIdentifier)
		test.ErrorIs(t, store.CreateAccessToken(ctx, nil), oauth2server.ErrNilRecord)

		empty := newAccessToken(future, unique("family"))
		empty.Hash = ""
		test.ErrorIs(t, store.CreateAccessToken(ctx, empty), oauth2server.ErrEmptyIdentifier)
	})
}

//nolint:gocognit // one subtest per behavior; splitting further would separate a case from its name.
func runRefreshTokenCases(t *testing.T, newStore Factory) {
	t.Helper()

	t.Run("an issued refresh token round-trips through a rotation", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newRefreshToken(future, unique("family"))

		must.NoError(t, store.CreateRefreshToken(ctx, token))

		got, err := store.ConsumeRefreshToken(ctx, token.Hash)
		must.NoError(t, err)
		must.NotNil(t, got)

		test.EqOp(t, token.ClientID, got.ClientID)
		test.EqOp(t, token.FamilyID, got.FamilyID)
		test.EqOp(t, token.Subject.ID, got.Subject.ID)
		test.Eq(t, token.Scopes, got.Scopes)
		test.Eq(t, token.Audience, got.Audience)
	})

	t.Run("a replayed refresh token names its family", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		family := unique("family")
		token := newRefreshToken(future, family)

		must.NoError(t, store.CreateRefreshToken(ctx, token))

		_, err := store.ConsumeRefreshToken(ctx, token.Hash)
		must.NoError(t, err)

		replayed, err := store.ConsumeRefreshToken(ctx, token.Hash)
		test.ErrorIs(t, err, oauth2server.ErrAlreadyRedeemed)

		// Rotation without this detects nothing: the replay is refused, the
		// copy the attacker is actually using keeps working, and nobody finds
		// out. The family identifier is what turns the refusal into a
		// revocation.
		must.NotNil(t, replayed)
		test.EqOp(t, family, replayed.FamilyID)
	})

	t.Run("a revoked refresh token is expired rather than replayed", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newRefreshToken(future, unique("family"))

		must.NoError(t, store.CreateRefreshToken(ctx, token))
		must.NoError(t, store.RevokeRefreshToken(ctx, token.Hash))

		got, err := store.ConsumeRefreshToken(ctx, token.Hash)

		// It was never exchanged, so calling it a replay would report a reuse
		// attack every time somebody signs out and their client retries.
		test.ErrorIs(t, err, oauth2server.ErrExpired)
		test.False(t, isReplay(err))
		test.Nil(t, got)
	})

	t.Run("an expired refresh token is refused and is not thereby marked redeemed", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newRefreshToken(past, unique("family"))

		must.NoError(t, store.CreateRefreshToken(ctx, token))

		_, err := store.ConsumeRefreshToken(ctx, token.Hash)
		test.ErrorIs(t, err, oauth2server.ErrExpired)

		_, err = store.ConsumeRefreshToken(ctx, token.Hash)
		test.ErrorIs(t, err, oauth2server.ErrExpired)
		test.False(t, isReplay(err))
	})

	t.Run("concurrent rotations of one refresh token produce exactly one success", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newRefreshToken(future, unique("family"))

		must.NoError(t, store.CreateRefreshToken(ctx, token))

		var (
			wg        sync.WaitGroup
			succeeded atomic.Int64
			replayed  atomic.Int64
			start     = make(chan struct{})
		)

		errs := make([]error, contenders)

		for i := range contenders {
			wg.Go(func() {
				<-start

				_, err := store.ConsumeRefreshToken(ctx, token.Hash)
				switch {
				case err == nil:
					succeeded.Add(1)
				case isReplay(err):
					replayed.Add(1)
				default:
					errs[i] = err
				}
			})
		}

		close(start)
		wg.Wait()

		for _, err := range errs {
			must.NoError(t, err)
		}

		// Two winners would mean two live refresh tokens in one family, which
		// is indistinguishable from the reuse this rotation exists to detect —
		// so the next honest refresh would revoke the user's own session.
		test.EqOp(t, int64(1), succeeded.Load())
		test.EqOp(t, int64(contenders-1), replayed.Load())
	})

	t.Run("reading a refresh token does not spend it", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newRefreshToken(future, unique("family"))

		must.NoError(t, store.CreateRefreshToken(ctx, token))

		got, err := store.GetRefreshToken(ctx, token.Hash)
		must.NoError(t, err)
		must.NotNil(t, got)
		test.EqOp(t, token.FamilyID, got.FamilyID)

		// /revoke reads a token to learn whose it is. If that read consumed it,
		// an honest rotation racing the sign-out would look like a replay, and
		// the endpoint that ends a session would be manufacturing evidence of
		// an attack.
		rotated, err := store.ConsumeRefreshToken(ctx, token.Hash)
		must.NoError(t, err)
		must.NotNil(t, rotated)
	})

	t.Run("a redeemed refresh token is still readable, and a revoked one is not", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		redeemed := newRefreshToken(future, unique("family"))
		revoked := newRefreshToken(future, unique("family"))

		must.NoError(t, store.CreateRefreshToken(ctx, redeemed))
		must.NoError(t, store.CreateRefreshToken(ctx, revoked))

		_, err := store.ConsumeRefreshToken(ctx, redeemed.Hash)
		must.NoError(t, err)

		// A sign-out arriving after a rotation is the ordinary case, and the
		// family the spent token names is exactly what has to be revoked.
		got, err := store.GetRefreshToken(ctx, redeemed.Hash)
		must.NoError(t, err)
		must.NotNil(t, got)
		test.EqOp(t, redeemed.FamilyID, got.FamilyID)

		must.NoError(t, store.RevokeRefreshToken(ctx, revoked.Hash))

		_, err = store.GetRefreshToken(ctx, revoked.Hash)
		test.ErrorIs(t, err, oauth2server.ErrExpired)
	})

	t.Run("an absent refresh token is ErrNotFound", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)

		// Distinct from the expired and revoked cases above, and the
		// distinction reaches /revoke: a token nobody issued is answered the
		// same way as one that was, and a store reporting a fault for it would
		// turn that endpoint into a 500.
		got, err := store.GetRefreshToken(ctx, oauth2server.Hash(unique("nothing")))
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
		test.Nil(t, got)

		// And consuming one, which is the path /token takes. A rotation of a
		// token that does not exist is a refusal, not a replay — calling it
		// reuse would revoke a family over a credential this server never
		// issued.
		rotated, err := store.ConsumeRefreshToken(ctx, oauth2server.Hash(unique("nothing")))
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
		test.False(t, stderrors.Is(err, oauth2server.ErrAlreadyRedeemed))
		test.Nil(t, rotated)
	})

	t.Run("a second refresh token under one hash is refused", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newRefreshToken(future, unique("family"))

		must.NoError(t, store.CreateRefreshToken(ctx, token))

		second := newRefreshToken(future, unique("family"))
		second.Hash = token.Hash

		test.ErrorIs(t, store.CreateRefreshToken(ctx, second), oauth2server.ErrRecordExists)

		got, err := store.GetRefreshToken(ctx, token.Hash)
		must.NoError(t, err)
		test.EqOp(t, token.FamilyID, got.FamilyID)
	})

	t.Run("an empty hash is refused rather than matching a row", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)

		_, err := store.ConsumeRefreshToken(ctx, "")
		test.ErrorIs(t, err, oauth2server.ErrEmptyIdentifier)

		_, err = store.GetRefreshToken(ctx, "")
		test.ErrorIs(t, err, oauth2server.ErrEmptyIdentifier)

		test.ErrorIs(t, store.RevokeRefreshToken(ctx, ""), oauth2server.ErrEmptyIdentifier)
		test.ErrorIs(t, store.CreateRefreshToken(ctx, nil), oauth2server.ErrNilRecord)

		empty := newRefreshToken(future, unique("family"))
		empty.Hash = ""
		test.ErrorIs(t, store.CreateRefreshToken(ctx, empty), oauth2server.ErrEmptyIdentifier)
	})
}

func runFamilyCases(t *testing.T, newStore Factory) {
	t.Helper()

	t.Run("revoking a family reaches both kinds of token and stops at its edge", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		doomed, spared := unique("doomed"), unique("spared")

		doomedAccess := newAccessToken(future, doomed)
		doomedRefresh := newRefreshToken(future, doomed)
		sparedAccess := newAccessToken(future, spared)
		sparedRefresh := newRefreshToken(future, spared)

		must.NoError(t, store.CreateAccessToken(ctx, doomedAccess))
		must.NoError(t, store.CreateRefreshToken(ctx, doomedRefresh))
		must.NoError(t, store.CreateAccessToken(ctx, sparedAccess))
		must.NoError(t, store.CreateRefreshToken(ctx, sparedRefresh))

		revoked, err := store.RevokeFamily(ctx, doomed)
		must.NoError(t, err)
		test.EqOp(t, int64(2), revoked)

		_, err = store.GetAccessToken(ctx, doomedAccess.Hash)
		test.ErrorIs(t, err, oauth2server.ErrExpired)

		_, err = store.ConsumeRefreshToken(ctx, doomedRefresh.Hash)
		test.ErrorIs(t, err, oauth2server.ErrExpired)

		// A reuse in one family is not evidence about another, and a store
		// that revoked by subject rather than by family would sign the user
		// out of every device over one stolen token.
		got, err := store.GetAccessToken(ctx, sparedAccess.Hash)
		must.NoError(t, err)
		must.NotNil(t, got)

		kept, err := store.ConsumeRefreshToken(ctx, sparedRefresh.Hash)
		must.NoError(t, err)
		must.NotNil(t, kept)
	})

	t.Run("revoking a family twice revokes nothing the second time", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		family := unique("family")

		must.NoError(t, store.CreateAccessToken(ctx, newAccessToken(future, family)))

		first, err := store.RevokeFamily(ctx, family)
		must.NoError(t, err)
		test.EqOp(t, int64(1), first)

		// The count is for a metric, not for control flow, so it reports what
		// this call changed rather than what the family contains.
		second, err := store.RevokeFamily(ctx, family)
		must.NoError(t, err)
		test.EqOp(t, int64(0), second)
	})

	t.Run("revoking an unknown family is not an error", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)

		revoked, err := store.RevokeFamily(ctx, unique("never_issued"))
		must.NoError(t, err)
		test.EqOp(t, int64(0), revoked)

		_, err = store.RevokeFamily(ctx, "")
		test.ErrorIs(t, err, oauth2server.ErrEmptyIdentifier)
	})
}

func runSweepCases(t *testing.T, newStore Factory) {
	t.Helper()

	t.Run("a sweep removes what is past its deadline and nothing else", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)

		deadCode, liveCode := newCode(longPast), newCode(future)
		deadAccess, liveAccess := newAccessToken(longPast, unique("f")), newAccessToken(future, unique("f"))
		deadRefresh, liveRefresh := newRefreshToken(longPast, unique("f")), newRefreshToken(future, unique("f"))
		deadClient, liveClient := newClient(longPast), newClient(future)

		must.NoError(t, store.CreateAuthorizationCode(ctx, deadCode))
		must.NoError(t, store.CreateAuthorizationCode(ctx, liveCode))
		must.NoError(t, store.CreateAccessToken(ctx, deadAccess))
		must.NoError(t, store.CreateAccessToken(ctx, liveAccess))
		must.NoError(t, store.CreateRefreshToken(ctx, deadRefresh))
		must.NoError(t, store.CreateRefreshToken(ctx, liveRefresh))
		must.NoError(t, store.CreateClient(ctx, deadClient))
		must.NoError(t, store.CreateClient(ctx, liveClient))

		// The count is deliberately not asserted. A shared database serves every
		// subtest at once, so another one's dead rows land in this number — and
		// another subtest's sweep may already have taken these ones, which is
		// correct behavior and would make any bound wrong. What a sweep did is
		// asserted by what is left, which is the thing a caller can observe.
		// The exact count belongs in a per-implementation test with a database
		// to itself.
		_, err := store.Sweep(ctx, sweepAt())
		must.NoError(t, err)

		_, err = store.ConsumeAuthorizationCode(ctx, deadCode.Hash)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)

		got, err := store.ConsumeAuthorizationCode(ctx, liveCode.Hash)
		must.NoError(t, err)
		must.NotNil(t, got)

		live, err := store.GetAccessToken(ctx, liveAccess.Hash)
		must.NoError(t, err)
		must.NotNil(t, live)

		kept, err := store.GetClient(ctx, liveClient.ID)
		must.NoError(t, err)
		must.NotNil(t, kept)
	})

	t.Run("a sweep keeps a revoked token that has not expired", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newStore(t)
		token := newAccessToken(future, unique("family"))

		must.NoError(t, store.CreateAccessToken(ctx, token))
		must.NoError(t, store.RevokeAccessToken(ctx, token.Hash))

		_, err := store.Sweep(ctx, sweepAt())
		must.NoError(t, err)

		// Deleting it would turn "you signed out" into "no such token", which
		// is the difference between a log line an operator can act on and one
		// that looks like a client bug.
		_, err = store.GetAccessToken(ctx, token.Hash)
		test.ErrorIs(t, err, oauth2server.ErrExpired)
		test.ErrorIs(t, err, oauth2server.ErrNotFound)
	})

	t.Run("a sweep with nothing to remove is not an error", func(t *testing.T) {
		t.Parallel()

		// The far past, so nothing any subtest wrote is inside the predicate
		// and the count is this sweep's alone even on a shared database.
		swept, err := newStore(t).Sweep(t.Context(), time.Unix(0, 0).UTC())
		must.NoError(t, err)
		test.EqOp(t, int64(0), swept)
	})
}

// isReplay reports whether err is the replay outcome specifically, rather than
// the ErrNotFound it wraps. The suite branches on it as well as asserting it:
// the concurrent cases have to count replays without failing on them.
func isReplay(err error) bool {
	return stderrors.Is(err, oauth2server.ErrAlreadyRedeemed)
}

// sweepAt is the instant the sweep cases sweep at: far enough back to reach
// what they wrote and not what any other case did. See sweepHorizon.
func sweepAt() time.Time {
	return time.Now().UTC().Add(sweepHorizon)
}

// unique returns an identifier no other subtest, parallel run, or rerun will
// produce, so one shared database can serve all of them.
func unique(prefix string) string {
	return prefix + "_" + identifiers.New()
}

// newClient builds a registration whose deadline is offset from now.
func newClient(offset time.Duration) *oauth2server.Client {
	now := time.Now().UTC().Truncate(time.Microsecond)

	return &oauth2server.Client{
		CreatedAt:               now,
		ExpiresAt:               now.Add(offset),
		ID:                      unique("client"),
		SecretHash:              oauth2server.Hash(unique("secret")),
		Name:                    "Conformance Client",
		RedirectURIs:            []string{"https://client.example/callback", "http://127.0.0.1:8080/cb"},
		GrantTypes:              []string{oauth2server.GrantTypeAuthorizationCode, oauth2server.GrantTypeRefreshToken},
		ResponseTypes:           []string{oauth2server.ResponseTypeCode},
		Scopes:                  []string{"read", "write"},
		TokenEndpointAuthMethod: oauth2server.AuthMethodClientSecret,
	}
}

// newCode builds an authorization code whose deadline is offset from now.
func newCode(offset time.Duration) *oauth2server.AuthorizationCode {
	now := time.Now().UTC().Truncate(time.Microsecond)

	return &oauth2server.AuthorizationCode{
		IssuedAt:      now,
		ExpiresAt:     now.Add(offset),
		Hash:          oauth2server.Hash(unique("code")),
		ClientID:      unique("client"),
		FamilyID:      unique("family"),
		RedirectURI:   "https://client.example/callback",
		CodeChallenge: oauth2server.S256Challenge(unique("verifier")),
		Nonce:         unique("nonce"),
		Subject:       testSubject(),
		Scopes:        []string{"read"},
		Resources:     []string{"https://api.example/"},
	}
}

// newAccessToken builds an access token in family whose deadline is offset from
// now.
func newAccessToken(offset time.Duration, family string) *oauth2server.AccessToken {
	now := time.Now().UTC().Truncate(time.Microsecond)

	return &oauth2server.AccessToken{
		IssuedAt:  now,
		ExpiresAt: now.Add(offset),
		Hash:      oauth2server.Hash(unique("access")),
		ClientID:  unique("client"),
		FamilyID:  family,
		Subject:   testSubject(),
		Scopes:    []string{"read"},
		Audience:  []string{"https://api.example/"},
	}
}

// newRefreshToken builds a refresh token in family whose deadline is offset
// from now.
func newRefreshToken(offset time.Duration, family string) *oauth2server.RefreshToken {
	now := time.Now().UTC().Truncate(time.Microsecond)

	return &oauth2server.RefreshToken{
		IssuedAt:  now,
		ExpiresAt: now.Add(offset),
		Hash:      oauth2server.Hash(unique("refresh")),
		ClientID:  unique("client"),
		FamilyID:  family,
		Subject:   testSubject(),
		Scopes:    []string{"read"},
		Audience:  []string{"https://api.example/"},
		Resources: []string{"https://api.example/"},
	}
}

// testSubject is the identity every record in this suite carries, including the
// application-shaped claims a store must round-trip without interpreting.
func testSubject() oauth2server.Subject {
	return oauth2server.Subject{
		ID:     unique("user"),
		Claims: map[string]string{"account_id": unique("account"), "role": "member"},
	}
}
