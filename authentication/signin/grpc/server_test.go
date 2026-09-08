package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"

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
			_ signingrpc.Principal            = (*testPrincipal)(nil)
			_ signingrpc.PrincipalExtractor   = extractPrincipal
			_ identitygrpc.PrincipalExtractor = extractPrincipal
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
