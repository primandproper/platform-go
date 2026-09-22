package signin_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/idempotency"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestService_LoginForToken_RefreshToken(T *testing.T) {
	T.Parallel()

	// The mint a sign-in makes, and the three things it puts on the answer.
	T.Run("mints a refresh token beside the access token", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)
		must.NotNil(t, signedIn)

		test.NotEqOp(t, "", signedIn.RefreshToken)
		test.NotEqOp(t, "", signedIn.FamilyID)
		test.NotEqOp(t, signedIn.Token, signedIn.RefreshToken)

		// The refresh token outlives the access token, which is the whole
		// arrangement: the short one is replaced without a password until the
		// long one lapses.
		test.True(t, signedIn.RefreshTokenExpiresAt.After(signedIn.ExpiresAt))
	})

	// The login is what the claim names, so a consumer's interceptor can check a
	// token against a revocation rather than only against a signature.
	T.Run("the family reaches the token's claims", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		test.EqOp[any](t, signedIn.FamilyID, e.issuer.claims[signin.ClaimFamilyID])

		// And the wire spelling is the conventional one, which is the one
		// translation this package makes between its vocabulary and a client's.
		test.EqOp(t, "sid", signin.ClaimFamilyID)
	})

	// The reason the mint is inside the login transaction rather than beside the
	// access token: a consumer recording a sign-in has to see the login it is
	// recording.
	T.Run("the family is visible to the issue hook", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		must.SliceLen(t, 1, e.hooks.signIns)
		test.EqOp(t, signedIn.FamilyID, e.hooks.signIns[0].FamilyID)
		test.NotEqOp(t, "", e.hooks.signIns[0].RefreshToken)
	})

	// The other half of one transaction: a hook that refuses takes the refresh
	// token with it, rather than leaving a credential outstanding for a sign-in
	// that never happened.
	T.Run("a hook that fails leaves no refresh token behind", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)
		e.hooks.issueErr = errRecordingFailed

		_, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.ErrorIs(t, err, errRecordingFailed)

		var rows int
		must.NoError(t, e.client.Writer().QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM "+refreshTable(t, e)).Scan(&rows))

		test.EqOp(t, 0, rows)
	})

	// The administrative door mints one too, on its own lifetime.
	T.Run("the administrative door mints against the administrative lifetime", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t, signin.WithAdminServiceRoles("service_admin"))
		e.setServiceRoles(t, "service_admin")
		secret := e.enrollTOTP(t)

		credentials := e.credentials()
		credentials.TOTPCode = code(t, secret)

		signedIn, err := e.svc.AdminLoginForToken(t.Context(), testScope, credentials)
		must.NoError(t, err)

		test.True(t, signedIn.Administrative)
		test.NotEqOp(t, "", signedIn.RefreshToken)

		// Twelve hours rather than thirty days — the sign-in that can ban a user
		// is the one worth stealing. The two deadlines are stamped from two
		// clock reads a few microseconds apart, so what is asserted is the
		// distance between them rather than an exact instant.
		gap := signedIn.RefreshTokenExpiresAt.Sub(signedIn.ExpiresAt)
		want := signin.DefaultAdminRefreshTokenTTL - signin.DefaultAdminTokenTTL

		test.True(t, gap > want-time.Second && gap < want+time.Second,
			test.Sprintf("refresh expiry is %s past the token's, wanted %s", gap, want))
	})

	// The door is a column on the row rather than something an exchange could be
	// told, and this is what it buys. Without it the first refresh would hand an
	// administrative session an ordinary token on an ordinary lifetime — the
	// hardening undone silently, and in the direction that lengthens it.
	T.Run("an exchanged administrative session stays administrative and stays short", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t, signin.WithAdminServiceRoles("service_admin"))
		e.setServiceRoles(t, "service_admin")
		secret := e.enrollTOTP(t)

		credentials := e.credentials()
		credentials.TOTPCode = code(t, secret)

		first, err := e.svc.AdminLoginForToken(t.Context(), testScope, credentials)
		must.NoError(t, err)
		must.True(t, first.Administrative)

		second, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.NoError(t, err)

		test.True(t, second.Administrative)
		test.EqOp(t, signin.DefaultAdminTokenTTL, e.issuer.expiry)

		gap := second.RefreshTokenExpiresAt.Sub(second.ExpiresAt)
		want := signin.DefaultAdminRefreshTokenTTL - signin.DefaultAdminTokenTTL

		test.True(t, gap > want-time.Second && gap < want+time.Second,
			test.Sprintf("refresh expiry is %s past the token's, wanted %s", gap, want))
	})

	// The default shape, unchanged: one token per sign-in and no table.
	T.Run("a service with no store mints no refresh token and still names a login", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		test.EqOp(t, "", signedIn.RefreshToken)
		test.True(t, signedIn.RefreshTokenExpiresAt.IsZero())
		test.NotEqOp(t, "", signedIn.FamilyID)
	})
}

func TestService_ExchangeRefreshToken(T *testing.T) {
	T.Parallel()

	// The rotation: one exchange, one successor, one login.
	T.Run("rotates into the same family", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		second, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.NoError(t, err)
		must.NotNil(t, second)

		test.EqOp(t, first.FamilyID, second.FamilyID)
		test.NotEqOp(t, first.RefreshToken, second.RefreshToken)
		test.EqOp(t, e.user.ID, second.Principal.User.ID)
		test.EqOp(t, e.accountID, second.Principal.ActiveAccountID)

		// A fresh access token, on the ordinary lifetime rather than on whatever
		// remained of the first one.
		test.EqOp(t, e.user.ID, e.issuer.subject)
		test.EqOp(t, signin.DefaultTokenTTL, e.issuer.expiry)
		test.EqOp[any](t, second.FamilyID, e.issuer.claims[signin.ClaimFamilyID])
	})

	// The account is the row's rather than re-resolved, which is what stops an
	// exchange quietly moving somebody into whatever their default has become.
	T.Run("keeps the account that was proven at sign-in", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		second := e.addAccount(t, "Second")

		credentials := e.credentials()
		credentials.ActiveAccountID = second

		first, err := e.svc.LoginForToken(t.Context(), testScope, credentials)
		must.NoError(t, err)
		must.EqOp(t, second, first.Principal.ActiveAccountID)

		rotated, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.NoError(t, err)

		test.EqOp(t, second, rotated.Principal.ActiveAccountID)
		test.EqOp[any](t, second, e.issuer.claims[signin.ClaimAccountID])
	})

	// The acceptance criterion the whole mechanism exists for.
	T.Run("a second exchange of one token ends the login", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		successor, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.NoError(t, err)

		// The replay. Whoever sent it is refused, and so is everybody else in
		// this family.
		replayed, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		test.Nil(t, replayed)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)

		after, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, successor.RefreshToken)
		test.Nil(t, after)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.False(t, platformerrors.Is(err, signin.ErrRefreshTokenReused))
	})

	// Every other refusal is the one sentinel, for the reason the password door
	// collapses its four.
	T.Run("collapses every other refusal", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, "never-minted")
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), tenancy.Of("dir_2"), signedIn.RefreshToken)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	})

	// The re-resolution is the point: a family must not outlive a ban, or a
	// suspension takes effect whenever the access token happens to expire. The
	// sentinel is the directory's rather than this package's, because
	// identity.Store.GetPrincipal refuses a banned user instead of answering with
	// a Principal for this package to inspect — so the check inside the exchange
	// is reached only by a Directory that does not enforce status of its own.
	T.Run("refuses a subject the directory will no longer admit", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		e.setStatus(t, identity.StatusBanned, "spam")

		rotated, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		test.Nil(t, rotated)
		test.ErrorIs(t, err, identity.ErrSignInNotAdmitted)
	})

	// A Directory that hands back a Principal for somebody the operator suspended
	// must not get them a token because this package assumed identity refused
	// first. The seam is an interface, so the check after the re-read is not
	// dead code — it is the half that answers for a directory that is not
	// identity.Store, and it is the half that still names which of the three
	// statuses it was.
	T.Run("refuses a directory that admits a banned subject itself", func(t *testing.T) {
		t.Parallel()

		e := newPermissiveRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		e.setStatus(t, identity.StatusBanned, "spam")

		rotated, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		test.Nil(t, rotated)
		test.ErrorIs(t, err, signin.ErrUserBanned)
	})

	// Nobody proved a credential here, so an access log that recorded a refresh
	// as an authentication would report a password that was never typed.
	T.Run("runs the issue hook and not the authentication hook", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		must.Eq(t, []string{"authenticate", "issue"}, e.hooks.calls)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		must.NoError(t, err)

		test.Eq(t, []string{"authenticate", "issue", "issue"}, e.hooks.calls)
	})

	// One transaction: a hook that refuses takes the rotation with it, so the
	// presented token is still spendable rather than spent for nothing.
	T.Run("a hook that fails leaves the presented token spendable", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		e.hooks.issueErr = errRecordingFailed

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		must.ErrorIs(t, err, errRecordingFailed)

		e.hooks.issueErr = nil

		rotated, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		must.NoError(t, err)
		test.NotEqOp(t, "", rotated.RefreshToken)
	})

	T.Run("refuses an exchange that presented nothing", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		_, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, "anything")
		test.NotNil(t, err)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, "")
		test.ErrorIs(t, err, signin.ErrEmptyRefreshToken)
	})

	T.Run("refuses a service that stores no refresh tokens", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, "anything")
		test.ErrorIs(t, err, signin.ErrRefreshTokensNotConfigured)
	})
}

func TestService_RevokeRefreshTokens(T *testing.T) {
	T.Parallel()

	// Sign-out: the login ends, and what is already in somebody's hands is not
	// replaced.
	T.Run("revoking a family ends that login", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		revoked, err := e.svc.RevokeRefreshTokenFamily(t.Context(), testScope, signedIn.FamilyID)
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	})

	// Sign out everywhere, which cannot be assembled out of family revocations:
	// a caller holding a user ID cannot enumerate that person's logins.
	T.Run("revoking a subject ends every login they hold", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		second, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		must.NotEqOp(t, first.FamilyID, second.FamilyID)

		revoked, err := e.svc.RevokeRefreshTokensForSubject(t.Context(), testScope, e.user.ID)
		must.NoError(t, err)
		test.EqOp(t, int64(2), revoked)

		for _, signedIn := range []*signin.SignIn{first, second} {
			_, exchangeErr := e.svc.ExchangeRefreshToken(t.Context(), testScope, signedIn.RefreshToken)
			test.ErrorIs(t, exchangeErr, signin.ErrInvalidCredentials)
		}
	})

	T.Run("refuses a revocation that named nothing", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		_, err := e.svc.RevokeRefreshTokenFamily(t.Context(), testScope, "")
		test.ErrorIs(t, err, signin.ErrEmptyFamilyID)

		_, err = e.svc.RevokeRefreshTokensForSubject(t.Context(), testScope, "")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
	})

	T.Run("refuses a service that stores no refresh tokens", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.RevokeRefreshTokenFamily(t.Context(), testScope, "family")
		test.ErrorIs(t, err, signin.ErrRefreshTokensNotConfigured)

		_, err = e.svc.RevokeRefreshTokensForSubject(t.Context(), testScope, e.user.ID)
		test.ErrorIs(t, err, signin.ErrRefreshTokensNotConfigured)
	})
}

func TestNewService_Lifetimes(T *testing.T) {
	T.Parallel()

	// The one relationship between the four lifetimes that is a mistake rather
	// than a preference. A client holding a working access token has no reason to
	// refresh, so by the time it does the family is already gone — which is why
	// this is refused at construction rather than at a call.
	T.Run("refuses a refresh token shorter lived than the token it mints", func(t *testing.T) {
		t.Parallel()

		for name, opts := range map[string][]signin.ServiceOption{
			"ordinary": {
				signin.WithTokenTTL(2 * time.Hour),
				signin.WithRefreshTokenTTL(time.Hour),
			},
			"administrative": {
				signin.WithAdminTokenTTL(2 * time.Hour),
				signin.WithAdminRefreshTokenTTL(time.Hour),
			},
		} {
			svc, err := newBareService(t, opts...)
			test.Nil(t, svc, test.Sprintf("case %q", name))
			test.ErrorIs(t, err, signin.ErrRefreshTokenTTLTooShort, test.Sprintf("case %q", name))
		}
	})

	// Equality is a deployment's choice rather than an error: a refresh token
	// exactly as long lived as its access token still gives a client the whole of
	// that window to use it.
	T.Run("accepts lifetimes that are equal", func(t *testing.T) {
		t.Parallel()

		svc, err := newBareService(t,
			signin.WithTokenTTL(time.Hour),
			signin.WithRefreshTokenTTL(time.Hour),
			signin.WithAdminTokenTTL(time.Minute),
			signin.WithAdminRefreshTokenTTL(time.Minute),
		)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("the defaults are consistent with each other", func(t *testing.T) {
		t.Parallel()

		svc, err := newBareService(t)
		must.NoError(t, err)
		test.NotNil(t, svc)

		test.True(t, signin.DefaultRefreshTokenTTL > signin.DefaultTokenTTL)
		test.True(t, signin.DefaultAdminRefreshTokenTTL > signin.DefaultAdminTokenTTL)

		// The administrative pair is the shorter one, which is the same reading
		// the access tokens already take of the same question.
		test.True(t, signin.DefaultAdminRefreshTokenTTL < signin.DefaultRefreshTokenTTL)
	})
}

func TestDefaultClaims(T *testing.T) {
	T.Parallel()

	T.Run("emits the account, the directory and the login", func(t *testing.T) {
		t.Parallel()

		claims, err := signin.DefaultClaims(t.Context(), &signin.ClaimsInput{
			Principal: &identity.Principal{
				User:            &identity.User{ID: "user_1", Scope: testScope},
				ActiveAccountID: "account_1",
			},
			FamilyID: "family_1",
		})
		must.NoError(t, err)

		test.Eq(t, map[string]any{
			signin.ClaimAccountID: "account_1",
			signin.ClaimScope:     testScope.String(),
			signin.ClaimFamilyID:  "family_1",
		}, claims)
	})

	T.Run("refuses an input with nobody on it", func(t *testing.T) {
		t.Parallel()

		for name, input := range map[string]*signin.ClaimsInput{
			"no input":     nil,
			"no principal": {FamilyID: "family_1"},
		} {
			claims, err := signin.DefaultClaims(t.Context(), input)
			test.Nil(t, claims, test.Sprintf("case %q", name))
			test.ErrorIs(t, err, identity.ErrNilUser, test.Sprintf("case %q", name))
		}
	})
}

// errRecordingFailed is what a hook is made to return when the test is about
// what a refused hook rolls back.
var errRecordingFailed = platformerrors.New("the consumer could not record this sign-in")

// newBareService builds a service over a client and directory that are never
// used, for the assertions that are about construction alone.
func newBareService(t *testing.T, opts ...signin.ServiceOption) (*signin.Service, error) {
	t.Helper()

	e := newEnv(t)

	return signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer, opts...)
}

// refreshTable names the table the env's refresh token store writes to, for the
// two assertions that count rows rather than ask the service.
func refreshTable(t *testing.T, e *env) string {
	t.Helper()

	must.NotNil(t, e.refresh)

	return e.refreshPrefix + "_signin_refresh_tokens"
}

// plainRefreshStore is a RefreshTokenStore and nothing more: the four methods,
// with the idempotent pair deliberately unreachable.
//
// It is what a consumer who implemented the seam themselves has, and it exists
// so that "a store without the interface behaves exactly as it does today" is a
// test rather than a sentence. Embedding the interface rather than the SQL store
// is what makes the assertion true by construction — a method added to
// IdempotentRefreshTokenStore cannot be promoted onto this by accident.
type plainRefreshStore struct {
	signin.RefreshTokenStore
}

func TestService_ExchangeRefreshToken_Idempotently(T *testing.T) {
	T.Parallel()

	// The case rotation had no answer for: the response never arrived, so the
	// client has no successor to retry with and re-sends what it still holds.
	T.Run("a retry carrying the key that spent the token is not a reuse", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		// Minted once, outside the retry loop, which is the client rule the
		// whole mechanism rests on.
		ctx := idempotency.WithKey(t.Context(), "exchange_01")

		lost, err := e.svc.ExchangeRefreshToken(ctx, testScope, first.RefreshToken)
		must.NoError(t, err)

		retried, err := e.svc.ExchangeRefreshToken(ctx, testScope, first.RefreshToken)
		must.NoError(t, err)
		must.NotNil(t, retried)

		// A fresh successor rather than the one the lost answer carried, in the
		// same login.
		test.EqOp(t, first.FamilyID, retried.FamilyID)
		test.NotEqOp(t, lost.RefreshToken, retried.RefreshToken)

		// And it works, which is the whole deliverable.
		after, err := e.svc.ExchangeRefreshToken(ctx, testScope, retried.RefreshToken)
		test.NoError(t, err)
		test.NotNil(t, after)
	})

	// The successor the client never received is revoked rather than left live,
	// so one login still holds exactly one exchangeable token.
	T.Run("the successor the lost answer carried stops working", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		ctx := idempotency.WithKey(t.Context(), "exchange_01")

		lost, err := e.svc.ExchangeRefreshToken(ctx, testScope, first.RefreshToken)
		must.NoError(t, err)

		_, err = e.svc.ExchangeRefreshToken(ctx, testScope, first.RefreshToken)
		must.NoError(t, err)

		// Revoked, so it is the ordinary refusal rather than a detected theft —
		// nobody replayed anything.
		dead, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, lost.RefreshToken)
		test.Nil(t, dead)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.False(t, platformerrors.Is(err, signin.ErrRefreshTokenReused))
	})

	// The bound. One captured request must not become a renewable session.
	T.Run("a key buys one retry", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		ctx := idempotency.WithKey(t.Context(), "exchange_01")

		_, err = e.svc.ExchangeRefreshToken(ctx, testScope, first.RefreshToken)
		must.NoError(t, err)

		_, err = e.svc.ExchangeRefreshToken(ctx, testScope, first.RefreshToken)
		must.NoError(t, err)

		replayed, err := e.svc.ExchangeRefreshToken(ctx, testScope, first.RefreshToken)
		test.Nil(t, replayed)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)
	})

	// A thief's replay carries no key, and gets the answer it has always got.
	T.Run("a replay without the key still ends the login", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		ctx := idempotency.WithKey(t.Context(), "exchange_01")

		successor, err := e.svc.ExchangeRefreshToken(ctx, testScope, first.RefreshToken)
		must.NoError(t, err)

		replayed, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		test.Nil(t, replayed)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)

		after, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, successor.RefreshToken)
		test.Nil(t, after)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	})

	// Both halves of "additive": no key, or no interface, and the exchange is the
	// one that shipped.
	T.Run("a request with no key takes the ordinary path", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		must.NoError(t, err)

		replayed, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		test.Nil(t, replayed)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)
	})

	T.Run("a store that does not implement the interface takes the ordinary path", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		// A second service over the same database and the same user, differing
		// from the suite's in one thing: the store it was handed cannot record a
		// key.
		svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer,
			signin.WithRefreshTokenStore(plainRefreshStore{RefreshTokenStore: e.refresh}))
		must.NoError(t, err)

		first, err := svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		ctx := idempotency.WithKey(t.Context(), "exchange_01")

		_, err = svc.ExchangeRefreshToken(ctx, testScope, first.RefreshToken)
		must.NoError(t, err)

		// The key was sent and nothing recorded it, so the retry is the reuse it
		// has always been. That is the honest answer for a store that cannot do
		// better, and it is why the key is not a promise the service makes on its
		// own.
		replayed, err := svc.ExchangeRefreshToken(ctx, testScope, first.RefreshToken)
		test.Nil(t, replayed)
		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)
	})

	// A malformed key is refused rather than dropped: a client told its retry was
	// protected when nothing recorded it is worse off than one told to fix its
	// header.
	T.Run("a malformed key is refused and spends nothing", func(t *testing.T) {
		t.Parallel()

		e := newRefreshEnv(t)

		first, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		ctx := idempotency.WithKey(t.Context(), "exchange one")

		refused, err := e.svc.ExchangeRefreshToken(ctx, testScope, first.RefreshToken)
		test.Nil(t, refused)
		test.ErrorIs(t, err, idempotency.ErrKeyInvalid)

		// The token is untouched, so the client can correct itself and carry on.
		rotated, err := e.svc.ExchangeRefreshToken(t.Context(), testScope, first.RefreshToken)
		test.NoError(t, err)
		test.NotNil(t, rotated)
	})
}
