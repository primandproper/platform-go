package signin_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/argon2"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewService(T *testing.T) {
	T.Parallel()

	T.Run("nil client", func(t *testing.T) {
		t.Parallel()

		svc, err := signin.NewService(nil, nil, nil, nil)
		test.Nil(t, svc)
		test.ErrorIs(t, err, signin.ErrNilDatabaseClient)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("the three other nils, each named", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := signin.NewService(e.client, nil, argon2.NewArgon2Authenticator(), e.issuer)
		test.ErrorIs(t, err, signin.ErrNilDirectory)

		_, err = signin.NewService(e.client, e.store, nil, e.issuer)
		test.ErrorIs(t, err, signin.ErrNilAuthenticator)

		_, err = signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), nil)
		test.ErrorIs(t, err, signin.ErrNilTokenIssuer)
	})

	T.Run("identity.Store satisfies Directory", func(t *testing.T) {
		t.Parallel()

		// The compile-time claim this package's seam rests on: a consumer hands
		// over the store they already have.
		var _ signin.Directory = (*identity.SQLStore)(nil)
	})
}

func TestService_LoginForToken(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)
		must.NotNil(t, signedIn)

		test.EqOp(t, "token-for-"+e.user.ID, signedIn.Token)
		test.EqOp(t, "jti-"+e.user.ID, signedIn.TokenID)
		test.False(t, signedIn.Administrative)
		test.EqOp(t, e.accountID, signedIn.Principal.ActiveAccountID)

		// The issuer saw the subject, the default lifetime and the default
		// claims — the three things a consumer configures and can only observe
		// here.
		test.EqOp(t, e.user.ID, e.issuer.subject)
		test.EqOp(t, signin.DefaultTokenTTL, e.issuer.expiry)
		test.Eq(t, map[string]any{
			signin.ClaimAccountID: e.accountID,
			signin.ClaimScope:     testScope.String(),
		}, e.issuer.claims)

		must.SliceLen(t, 1, e.hooks.signIns)
		test.SliceEmpty(t, e.hooks.failures)
	})

	T.Run("by email address", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope,
			&signin.Credentials{EmailAddress: "jane@example.com", Password: e.password})
		must.NoError(t, err)
		test.EqOp(t, e.user.ID, signedIn.Principal.User.ID)
	})

	T.Run("the four collapsed refusals answer identically", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		secret := e.enrollTOTP(t)

		cases := map[string]*signin.Credentials{
			"unknown handle": {Username: "nobody", Password: e.password},
			"wrong password": {Username: "jane", Password: "not it", TOTPCode: code(t, secret)},
			"wrong code":     {Username: "jane", Password: e.password, TOTPCode: "000000"},
		}

		for name, credentials := range cases {
			signedIn, err := e.svc.LoginForToken(t.Context(), testScope, credentials)

			test.Nil(t, signedIn, test.Sprintf("%s returned a sign-in", name))
			test.ErrorIs(t, err, signin.ErrInvalidCredentials, test.Sprintf("%s", name))
		}
	})

	T.Run("a passwordless user gets the same refusal", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		passwordless := e.registerPasswordless(t, "passkeyonly")

		// identity.User.HasPassword is right that "you have no password" is a
		// different answer from "wrong password". This is the caller that
		// cannot afford to give the specific one: it would name a registered
		// user to whoever guessed a handle.
		signedIn, err := e.svc.LoginForToken(t.Context(), testScope,
			&signin.Credentials{Username: passwordless.Username, Password: "anything"})

		test.Nil(t, signedIn)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.False(t, errors.Is(err, signin.ErrNoPasswordCredential))
	})

	T.Run("the principal names the account the credentials asked for", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		credentials := e.credentials()
		credentials.ActiveAccountID = e.accountID

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, credentials)
		must.NoError(t, err)
		test.EqOp(t, e.accountID, signedIn.Principal.ActiveAccountID)
	})

	T.Run("an account the caller is not a member of is refused", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		credentials := e.credentials()
		credentials.ActiveAccountID = "acct_belonging_to_somebody_else"

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, credentials)
		test.Nil(t, signedIn)
		test.ErrorIs(t, err, identity.ErrMembershipNotFound)
	})

	T.Run("both handles is refused rather than resolved", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.LoginForToken(t.Context(), testScope, &signin.Credentials{
			Username:     "jane",
			EmailAddress: "jane@example.com",
			Password:     e.password,
		})
		test.ErrorIs(t, err, signin.ErrAmbiguousHandle)

		// Refused before anything was looked up, so no attempt was recorded.
		test.SliceEmpty(t, e.hooks.failures)
	})

	T.Run("neither handle, no password, nil credentials", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.LoginForToken(t.Context(), testScope, &signin.Credentials{Password: e.password})
		test.ErrorIs(t, err, signin.ErrEmptyHandle)

		_, err = e.svc.LoginForToken(t.Context(), testScope, &signin.Credentials{Username: "jane"})
		test.ErrorIs(t, err, signin.ErrEmptyPassword)

		_, err = e.svc.LoginForToken(t.Context(), testScope, nil)
		test.ErrorIs(t, err, signin.ErrNilCredentials)

		test.SliceEmpty(t, e.hooks.failures)
	})

	T.Run("an unknown handle still costs a hash", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		stub := &stubAuthenticator{}

		svc, err := signin.NewService(e.client, e.store, stub, e.issuer)
		must.NoError(t, err)

		_, err = svc.LoginForToken(t.Context(), testScope,
			&signin.Credentials{Username: "nobody", Password: "whatever"})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		// The timing defense, asserted as the only thing it can be asserted as:
		// the work was done. A handle that names nobody must not be cheaper
		// than one that does.
		test.EqOp(t, 1, stub.hashes)
		test.EqOp(t, 0, stub.matches)
	})

	T.Run("a hasher that fails on the decoy still refuses", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		stub := &stubAuthenticator{hashErr: errors.New("hasher is unwell")}

		svc, err := signin.NewService(e.client, e.store, stub, e.issuer)
		must.NoError(t, err)

		_, err = svc.LoginForToken(t.Context(), testScope,
			&signin.Credentials{Username: "nobody", Password: "whatever"})

		// The decoy's failure tells us nothing about these credentials, so it
		// does not become the answer.
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	})

	T.Run("a malformed stored hash refuses and keeps the cause", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		cause := errors.New("hash will not parse")
		stub := &stubAuthenticator{matchErr: cause}

		svc, err := signin.NewService(e.client, e.store, stub, e.issuer)
		must.NoError(t, err)

		_, err = svc.LoginForToken(t.Context(), testScope, e.credentials())

		// The caller is told the credentials proved nothing, which is true; an
		// operator is told the hash would not parse, which is what they need.
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.ErrorIs(t, err, cause)
	})

	T.Run("status is checked after the password", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			want        error
			status      identity.AccountStatus
			explanation string
		}{
			{want: signin.ErrUserBanned, status: identity.StatusBanned, explanation: "you were rude"},
			{want: signin.ErrUserTerminated, status: identity.StatusTerminated},
			{want: signin.ErrUserUnverified, status: identity.StatusUnverified},
		} {
			e := newEnv(t)
			e.setStatus(t, tc.status, tc.explanation)

			// A wrong password against a suspended user answers with the
			// credential refusal and never names the status, so a suspension is
			// not something an unauthenticated caller can enumerate.
			_, err := e.svc.LoginForToken(t.Context(), testScope,
				&signin.Credentials{Username: "jane", Password: "not it"})
			test.ErrorIs(t, err, signin.ErrInvalidCredentials)
			test.False(t, errors.Is(err, tc.want))

			// The right password reaches the status.
			_, err = e.svc.LoginForToken(t.Context(), testScope, e.credentials())
			test.ErrorIs(t, err, tc.want)
		}
	})

	T.Run("a banned user's explanation travels with the sentinel", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		e.setStatus(t, identity.StatusBanned, "you were rude")

		_, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.ErrorIs(t, err, signin.ErrUserBanned)
		test.StrContains(t, err.Error(), "you were rude")
	})

	T.Run("a proven second factor is asked for", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		secret := e.enrollTOTP(t)

		// No code: told to send one, which does disclose that the password was
		// right and is the one place this package accepts that.
		_, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		test.ErrorIs(t, err, signin.ErrSecondFactorRequired)

		credentials := e.credentials()
		credentials.TOTPCode = code(t, secret)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, credentials)
		must.NoError(t, err)
		test.NotEq(t, "", signedIn.Token)
	})

	T.Run("an unproven secret is not a second factor", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		// Issued and never verified, which is what RefreshTOTPSecret leaves
		// behind. The user signs in without a code.
		enrollment, err := e.svc.RefreshTOTPSecret(t.Context(), testScope, e.user.ID,
			&signin.SecretRefresh{CurrentPassword: e.password})
		must.NoError(t, err)
		must.NotNil(t, enrollment)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)
		test.NotEq(t, "", signedIn.Token)
	})

	T.Run("SecondFactorRequired refuses a user with none", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithSecondFactorPolicy(signin.SecondFactorRequired))

		_, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		test.ErrorIs(t, err, signin.ErrSecondFactorNotEnrolled)
	})

	T.Run("a refused attempt reaches the hook with its reason", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.LoginForToken(t.Context(), testScope,
			&signin.Credentials{Username: "jane", Password: "not it"})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		must.SliceLen(t, 1, e.hooks.failures)

		attempt := e.hooks.failures[0]
		test.EqOp(t, "jane", attempt.Handle)
		test.EqOp(t, e.user.ID, attempt.UserID)
		test.False(t, attempt.Administrative)
		test.ErrorIs(t, attempt.Reason, signin.ErrInvalidCredentials)
	})

	T.Run("an unknown handle reaches the hook with no user", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.LoginForToken(t.Context(), testScope,
			&signin.Credentials{Username: "nobody", Password: e.password})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		must.SliceLen(t, 1, e.hooks.failures)
		test.EqOp(t, "nobody", e.hooks.failures[0].Handle)
		test.EqOp(t, "", e.hooks.failures[0].UserID)
	})

	T.Run("a failing failure hook does not replace the refusal", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		hookErr := errors.New("the lockout counter is unwell")
		e.hooks.failedErr = hookErr

		_, err := e.svc.LoginForToken(t.Context(), testScope,
			&signin.Credentials{Username: "jane", Password: "not it"})

		// Both survive: the caller still matches the sentinel and the
		// consumer's failure is not swallowed.
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.ErrorIs(t, err, hookErr)
	})

	T.Run("a failing sign-in hook withholds the token", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		hookErr := errors.New("the audit log is unwell")
		e.hooks.signInErr = hookErr

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())

		// The token was minted and is not returned. A service that cannot
		// record a sign-in does not issue one.
		test.Nil(t, signedIn)
		test.ErrorIs(t, err, hookErr)
	})

	T.Run("an issuer that fails fails the sign-in", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		e.issuer.err = errors.New("no signing key")

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		test.Nil(t, signedIn)
		test.ErrorIs(t, err, e.issuer.err)
	})

	T.Run("a claims builder that fails fails the sign-in", func(t *testing.T) {
		t.Parallel()

		claimsErr := errors.New("cannot resolve the tenant plan")

		e := newEnv(t, signin.WithClaimsBuilder(
			func(context.Context, *identity.Principal) (map[string]any, error) { return nil, claimsErr },
		))

		_, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		test.ErrorIs(t, err, claimsErr)
		test.EqOp(t, 0, e.issuer.calls)
	})

	T.Run("a token's expiry is the lifetime that was asked for", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithTokenTTL(3*time.Minute))

		before := time.Now().UTC()

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		test.EqOp(t, 3*time.Minute, e.issuer.expiry)
		test.True(t, !signedIn.ExpiresAt.Before(before.Add(3*time.Minute)))
	})
}

func TestService_AdminLoginForToken(T *testing.T) {
	T.Parallel()

	T.Run("no administrative roles named means no administrative door", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		e.enrollTOTP(t)

		_, err := e.svc.AdminLoginForToken(t.Context(), testScope, e.credentials())
		test.ErrorIs(t, err, signin.ErrAdminLoginDisabled)
	})

	T.Run("a user without the role is refused", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithAdminServiceRoles("service_admin"))
		secret := e.enrollTOTP(t)

		credentials := e.credentials()
		credentials.TOTPCode = code(t, secret)

		_, err := e.svc.AdminLoginForToken(t.Context(), testScope, credentials)
		test.ErrorIs(t, err, signin.ErrNotAnAdministrator)
	})

	T.Run("an administrator without a second factor is refused whatever the policy", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithAdminServiceRoles("service_admin"))
		e.setServiceRoles(t, "service_admin")

		_, err := e.svc.AdminLoginForToken(t.Context(), testScope, e.credentials())
		test.ErrorIs(t, err, signin.ErrSecondFactorNotEnrolled)
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithAdminServiceRoles("service_admin"))
		e.setServiceRoles(t, "service_admin")

		secret := e.enrollTOTP(t)

		credentials := e.credentials()
		credentials.TOTPCode = code(t, secret)

		signedIn, err := e.svc.AdminLoginForToken(t.Context(), testScope, credentials)
		must.NoError(t, err)

		test.True(t, signedIn.Administrative)
		test.EqOp(t, signin.DefaultAdminTokenTTL, e.issuer.expiry)

		must.SliceLen(t, 1, e.hooks.signIns)
		test.True(t, e.hooks.signIns[0].Administrative)
	})

	T.Run("the role is checked after the password", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithAdminServiceRoles("service_admin"))

		_, err := e.svc.AdminLoginForToken(t.Context(), testScope,
			&signin.Credentials{Username: "jane", Password: "not it"})

		// "Is this person an administrator" is not a question an
		// unauthenticated caller gets to ask.
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.False(t, errors.Is(err, signin.ErrNotAnAdministrator))
	})

	T.Run("a failed administrative attempt is marked as one", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithAdminServiceRoles("service_admin"))
		e.setServiceRoles(t, "service_admin")
		e.enrollTOTP(t)

		credentials := e.credentials()
		credentials.TOTPCode = "000000"

		_, err := e.svc.AdminLoginForToken(t.Context(), testScope, credentials)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		must.SliceLen(t, 1, e.hooks.failures)
		test.True(t, e.hooks.failures[0].Administrative)
	})

	T.Run("empty role names are dropped rather than admitting everybody", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t, signin.WithAdminServiceRoles("", ""))
		e.enrollTOTP(t)

		// A consumer assembling the list from configuration with a blank line in
		// it gets no administrative door, not an open one.
		_, err := e.svc.AdminLoginForToken(t.Context(), testScope, e.credentials())
		test.ErrorIs(t, err, signin.ErrAdminLoginDisabled)
	})
}
