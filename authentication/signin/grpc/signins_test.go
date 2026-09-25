package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// signInAsJane opens one login through the password door and answers with the
// token it issued.
func (h *harness) signInAsJane(t *testing.T) *signinpb.IssuedToken {
	t.Helper()

	signedIn, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
		Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
	})
	must.NoError(t, err)

	return signedIn.GetToken()
}

func TestServer_ListSignIns(T *testing.T) {
	T.Parallel()

	T.Run("lists the caller's logins and marks the current one", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		phone := h.signInAsJane(t)
		laptop := h.signInAsJane(t)

		listed, err := h.client.ListSignIns(asUserIn(h.rootCtx, h.user.ID, laptop.GetFamilyId()), &signinpb.ListSignInsRequest{})
		must.NoError(t, err)
		must.SliceLen(t, 2, listed.GetSignIns())

		current := map[string]bool{}
		for _, signIn := range listed.GetSignIns() {
			current[signIn.GetFamilyId()] = signIn.GetCurrent()

			test.NotNil(t, signIn.GetSignedInAt())
			test.NotNil(t, signIn.GetLastRefreshedAt())
			test.NotNil(t, signIn.GetExpiresAt())
			test.EqOp(t, h.accountID, signIn.GetActiveAccountId())
		}

		test.Eq(t, map[string]bool{phone.GetFamilyId(): false, laptop.GetFamilyId(): true}, current)
	})

	// A consumer whose principal does not carry the claim is told nothing it
	// could mistake for an answer.
	T.Run("marks nothing current when the principal names no login", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		h.signInAsJane(t)

		listed, err := h.client.ListSignIns(h.asJane(), &signinpb.ListSignInsRequest{})
		must.NoError(t, err)
		must.SliceLen(t, 1, listed.GetSignIns())
		test.False(t, listed.GetSignIns()[0].GetCurrent())
	})

	T.Run("honors a limit", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		h.signInAsJane(t)
		h.signInAsJane(t)

		listed, err := h.client.ListSignIns(h.asJane(), &signinpb.ListSignInsRequest{Limit: 1})
		must.NoError(t, err)
		test.SliceLen(t, 1, listed.GetSignIns())
	})

	// The subject is the caller, so another user's logins are not reachable by
	// asking as them.
	T.Run("lists nobody else's logins", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		h.signInAsJane(t)

		listed, err := h.client.ListSignIns(asUser(h.rootCtx, "somebody_else"), &signinpb.ListSignInsRequest{})
		must.NoError(t, err)
		test.SliceEmpty(t, listed.GetSignIns())
	})

	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		_, err := h.client.ListSignIns(h.rootCtx, &signinpb.ListSignInsRequest{})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	// A service that stores no refresh tokens has no logins to list, and says
	// so as the wiring failure it is.
	T.Run("a service that stores no refresh tokens is a server error", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.client.ListSignIns(h.asJane(), &signinpb.ListSignInsRequest{})
		test.ErrorIs(t, err, signin.ErrRefreshTokensNotConfigured)
		test.EqOp(t, codes.Internal, status.Code(err))
	})
}

func TestServer_EndSignIn(T *testing.T) {
	T.Parallel()

	// The lost-phone case: the login is ended from another device, by name,
	// with no refresh token of its own in hand.
	T.Run("ends one of the caller's logins by name", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		phone := h.signInAsJane(t)
		laptop := h.signInAsJane(t)

		_, err := h.client.EndSignIn(asUserIn(h.rootCtx, h.user.ID, laptop.GetFamilyId()), &signinpb.EndSignInRequest{
			FamilyId: phone.GetFamilyId(),
		})
		must.NoError(t, err)

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: phone.GetRefreshToken(),
		})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: laptop.GetRefreshToken(),
		})
		test.NoError(t, err)
	})

	// A family identifier is not a secret. Somebody else presenting Jane's gets
	// the answer an unknown one gets, and Jane stays signed in.
	T.Run("cannot end somebody else's login", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		janes := h.signInAsJane(t)

		_, err := h.client.EndSignIn(asUser(h.rootCtx, "somebody_else"), &signinpb.EndSignInRequest{
			FamilyId: janes.GetFamilyId(),
		})
		must.NoError(t, err)

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: janes.GetRefreshToken(),
		})
		test.NoError(t, err)
	})

	T.Run("a request naming no login is invalid", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		_, err := h.client.EndSignIn(h.asJane(), &signinpb.EndSignInRequest{})
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		_, err := h.client.EndSignIn(h.rootCtx, &signinpb.EndSignInRequest{FamilyId: "family"})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}
