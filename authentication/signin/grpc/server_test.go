package grpc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/callers"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNewServer(T *testing.T) {
	T.Parallel()

	T.Run("nil service", func(t *testing.T) {
		t.Parallel()

		srv, err := signingrpc.NewServer(nil, nil)
		test.Nil(t, srv)
		test.ErrorIs(t, err, signingrpc.ErrNilService)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("nil principal extractor", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		srv, err := signingrpc.NewServer(h.svc, nil)
		test.Nil(t, srv)
		test.ErrorIs(t, err, signingrpc.ErrNilPrincipalExtractor)
	})

	T.Run("the principal seam is identity's", func(t *testing.T) {
		t.Parallel()

		// The alias is the whole point: a consumer writes one extractor and
		// both services read the same answer.
		// One function satisfies both names without a conversion, which is what
		// an alias gives and a second interface with the same three methods
		// would not.
		var (
			_ callers.Principal          = (*testPrincipal)(nil)
			_ callers.PrincipalExtractor = extractPrincipal
			_ callers.PrincipalExtractor = extractPrincipal
		)
	})

	T.Run("the default scope is global", func(t *testing.T) {
		t.Parallel()

		scope, err := signingrpc.GlobalScope(t.Context())
		must.NoError(t, err)
		test.EqOp(t, tenancy.Global(), scope)
	})
}

func TestServer_LoginForToken(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		response, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		must.NoError(t, err)
		must.NotNil(t, response.GetToken())

		test.EqOp(t, "token-for-"+h.user.ID, response.GetToken().GetToken())
		test.EqOp(t, "jti-"+h.user.ID, response.GetToken().GetTokenId())
		test.EqOp(t, h.accountID, response.GetToken().GetActiveAccountId())
		test.False(t, response.GetToken().GetAdministrative())
		test.NotNil(t, response.GetToken().GetExpiresAt())
	})

	T.Run("no principal is required", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		// h.rootCtx carries no credential, which is the whole point: this is
		// how a caller becomes somebody.
		_, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		test.NoError(t, err)
	})

	T.Run("a refusal crosses the wire as a sentinel and a code", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: "not it"},
		})

		// Both idioms, which is what the encoding and decoding interceptors
		// exist for. Neither is automatic.
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))

		// And the message is the sentinel's own words rather than the code's
		// name, which is what a client with no access to the details reads.
		test.EqOp(t, signin.ErrInvalidCredentials.Error(), status.Convert(err).Message())
	})

	T.Run("the refusals that share a code are told apart by their message", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		secret := h.enrollTOTP(t)
		_ = secret

		_, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})

		test.ErrorIs(t, err, signin.ErrSecondFactorRequired)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
		test.EqOp(t, signin.ErrSecondFactorRequired.Error(), status.Convert(err).Message())
	})

	T.Run("nil credentials", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{})
		test.ErrorIs(t, err, signingrpc.ErrNilCredentials)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("an issuer failure is not a credential failure", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithIssuer(t, &failingIssuer{}, nil)

		// The password is right. What failed is the step after proving it, and
		// a door whose default code was Unauthenticated would report this
		// outage as a wrong password — something the caller would try to fix by
		// retyping a credential that was never the problem.
		_, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		must.Error(t, err)

		test.NotEqOp(t, codes.Unauthenticated, status.Code(err))
		test.EqOp(t, codes.Internal, status.Code(err))
		test.False(t, errors.Is(err, signin.ErrInvalidCredentials))
	})

	T.Run("a scope resolver that refuses refuses the request", func(t *testing.T) {
		t.Parallel()

		resolverErr := platformerrors.New("no tenant on this connection")

		h := newHarness(t, nil, signingrpc.WithScopeResolver(
			func(context.Context) (tenancy.Scope, error) { return tenancy.Global(), resolverErr },
		))

		_, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestServer_AdminLoginForToken(T *testing.T) {
	T.Parallel()

	T.Run("no administrative door", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.client.AdminLoginForToken(h.rootCtx, &signinpb.AdminLoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		test.ErrorIs(t, err, signin.ErrAdminLoginDisabled)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, []signin.ServiceOption{signin.WithAdminServiceRoles("service_admin")})
		h.setServiceRoles(t, "service_admin")

		secret := h.enrollTOTP(t)

		response, err := h.client.AdminLoginForToken(h.rootCtx, &signinpb.AdminLoginForTokenRequest{
			Credentials: &signinpb.Credentials{
				Username: "jane",
				Password: h.password,
				TotpCode: totpCode(t, secret),
			},
		})
		must.NoError(t, err)
		test.True(t, response.GetToken().GetAdministrative())
	})

	T.Run("an issuer failure is not a credential failure", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithIssuer(t, &failingIssuer{},
			[]signin.ServiceOption{signin.WithAdminServiceRoles("service_admin")})
		h.setServiceRoles(t, "service_admin")

		secret := h.enrollTOTP(t)

		// The administrative door defaults the same way the anonymous one does,
		// and it has more to get wrong: a caller here proved a password and a
		// second factor before anything could fail.
		_, err := h.client.AdminLoginForToken(h.rootCtx, &signinpb.AdminLoginForTokenRequest{
			Credentials: &signinpb.Credentials{
				Username: "jane",
				Password: h.password,
				TotpCode: totpCode(t, secret),
			},
		})
		must.Error(t, err)

		test.NotEqOp(t, codes.Unauthenticated, status.Code(err))
		test.EqOp(t, codes.Internal, status.Code(err))
		test.False(t, errors.Is(err, signin.ErrInvalidCredentials))
	})
}

func TestServer_GetAuthStatus(T *testing.T) {
	T.Parallel()

	T.Run("an anonymous caller is answered rather than refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		response, err := h.client.GetAuthStatus(h.rootCtx, &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)

		// "Am I signed in" is a question whose answer can be no. Refusing it
		// would make every client treat its own first question as an error.
		test.False(t, response.GetAuthenticated())
		test.Nil(t, response.GetStatus())
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		response, err := h.client.GetAuthStatus(h.asJane(), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)

		must.True(t, response.GetAuthenticated())
		must.NotNil(t, response.GetStatus())

		authStatus := response.GetStatus()
		test.EqOp(t, h.user.ID, authStatus.GetUser().GetId())
		test.EqOp(t, h.accountID, authStatus.GetActiveAccountId())
		test.Eq(t, []string{h.accountID}, authStatus.GetAccountIds())
		test.True(t, authStatus.GetHasPassword())
		test.False(t, authStatus.GetTwoFactorEnrolled())
	})
}

func TestServer_GetSelf(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		response, err := h.client.GetSelf(h.asJane(), &signinpb.GetSelfRequest{})
		must.NoError(t, err)

		test.EqOp(t, h.user.ID, response.GetUser().GetId())
		test.EqOp(t, "jane", response.GetUser().GetUsername())
	})

	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.client.GetSelf(h.rootCtx, &signinpb.GetSelfRequest{})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestServer_CredentialWrites(T *testing.T) {
	T.Parallel()

	T.Run("update password", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.client.UpdatePassword(h.asJane(), &signinpb.UpdatePasswordRequest{
			CurrentPassword: h.password,
			NewPassword:     "a whole new password",
		})
		must.NoError(t, err)

		_, err = h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: "a whole new password"},
		})
		test.NoError(t, err)
	})

	T.Run("enrollment is the two calls together", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		refreshed, err := h.client.RefreshTOTPSecret(h.asJane(), &signinpb.RefreshTOTPSecretRequest{
			CurrentPassword: h.password,
		})
		must.NoError(t, err)
		must.StrNotEqFold(t, "", refreshed.GetSecret())
		test.StrContains(t, refreshed.GetProvisioningUri(), "otpauth://totp/")

		_, err = h.client.VerifyTOTPSecret(h.asJane(), &signinpb.VerifyTOTPSecretRequest{
			TotpCode: totpCode(t, refreshed.GetSecret()),
		})
		must.NoError(t, err)

		response, err := h.client.GetAuthStatus(h.asJane(), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.True(t, response.GetStatus().GetTwoFactorEnrolled())
	})

	T.Run("the three writes refuse an anonymous caller", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.client.UpdatePassword(h.rootCtx, &signinpb.UpdatePasswordRequest{})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)

		_, err = h.client.RefreshTOTPSecret(h.rootCtx, &signinpb.RefreshTOTPSecretRequest{})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)

		_, err = h.client.VerifyTOTPSecret(h.rootCtx, &signinpb.VerifyTOTPSecretRequest{})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
	})

	T.Run("a state refusal crosses as FailedPrecondition", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.client.VerifyTOTPSecret(h.asJane(), &signinpb.VerifyTOTPSecretRequest{TotpCode: "000000"})
		test.ErrorIs(t, err, signin.ErrSecondFactorNotEnrolled)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})
}

func TestServer_ExchangeRefreshToken(T *testing.T) {
	T.Parallel()

	// The round trip, and the three fields the conversion added to IssuedToken.
	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		first, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		must.NoError(t, err)
		must.NotEqOp(t, "", first.GetToken().GetRefreshToken())
		must.NotEqOp(t, "", first.GetToken().GetFamilyId())
		must.NotNil(t, first.GetToken().GetRefreshTokenExpiresAt())

		second, err := h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: first.GetToken().GetRefreshToken(),
		})
		must.NoError(t, err)

		// One login, two credentials: the family carries and the secret does
		// not.
		test.EqOp(t, first.GetToken().GetFamilyId(), second.GetToken().GetFamilyId())
		test.NotEqOp(t, first.GetToken().GetRefreshToken(), second.GetToken().GetRefreshToken())
		test.EqOp(t, h.accountID, second.GetToken().GetActiveAccountId())
	})

	// It is a door: a caller whose access token has expired has no credential to
	// present, so requiring one would make the RPC unreachable at exactly the
	// moment it is needed.
	T.Run("no principal is required", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		first, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		must.NoError(t, err)

		// h.rootCtx carries no credential.
		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: first.GetToken().GetRefreshToken(),
		})
		test.NoError(t, err)
	})

	// A replay answers exactly as a wrong password does, message included. The
	// sentinel survives the wire for the consumer's own logs, and the message
	// does not tell whoever sent it that their theft was noticed.
	T.Run("a replay is indistinguishable from any other refusal on the wire", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		first, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		must.NoError(t, err)

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: first.GetToken().GetRefreshToken(),
		})
		must.NoError(t, err)

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: first.GetToken().GetRefreshToken(),
		})

		test.ErrorIs(t, err, signin.ErrRefreshTokenReused)

		// Against the refusal it has to be indistinguishable from, rather than
		// against a word it must not contain. "Does not say 'already'" is
		// satisfied by every string that is not the right one — the handler's
		// own description among them — so it passes for a message that names
		// this endpoint and lets a thief tell a detected replay from a token
		// that was simply wrong.
		_, unknownErr := h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: "a-token-this-service-never-minted",
		})
		must.Error(t, unknownErr)

		test.EqOp(t, status.Code(unknownErr), status.Code(err))
		test.EqOp(t, status.Convert(unknownErr).Message(), status.Convert(err).Message())

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
		test.EqOp(t, signin.ErrInvalidCredentials.Error(), status.Convert(err).Message())
	})

	T.Run("an unknown token is the ordinary refusal", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		_, err := h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: "never-minted",
		})

		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	// A service that stores no refresh tokens has no such door, and a client
	// calling it anyway is a wiring failure rather than a request to correct.
	T.Run("a service with no store answers Internal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: "anything",
		})

		test.ErrorIs(t, err, signin.ErrRefreshTokensNotConfigured)
		test.EqOp(t, codes.Internal, status.Code(err))
	})

	// The default shape on the wire: both new fields empty, and the family set
	// anyway, because it names a sign-in rather than a stored row.
	T.Run("a service with no store leaves the refresh fields empty", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		response, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		must.NoError(t, err)

		test.EqOp(t, "", response.GetToken().GetRefreshToken())
		test.Nil(t, response.GetToken().GetRefreshTokenExpiresAt())
		test.NotEqOp(t, "", response.GetToken().GetFamilyId())
	})
}
