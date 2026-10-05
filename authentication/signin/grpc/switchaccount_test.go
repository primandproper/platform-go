package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v15/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v15/identity"

	"github.com/primandproper/primitives-go/v2/database"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// secondAccount creates an account in the harness's directory and returns its
// ID, putting the registered user in it where member says so.
func (h *harness) secondAccount(t *testing.T, member bool) string {
	t.Helper()

	var accountID string

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		created, err := h.store.CreateAccount(t.Context(), tx, testScope,
			&identity.Account{Name: "Second", Scope: testScope, OwnerUserID: h.user.ID})
		if err != nil {
			return err
		}

		accountID = created.ID

		if !member {
			return nil
		}

		_, err = h.store.CreateMembership(t.Context(), tx, testScope, &identity.Membership{
			BelongsToUser:    h.user.ID,
			BelongsToAccount: created.ID,
			Scope:            testScope,
			Roles:            []string{"owner"},
		})

		return err
	}))

	return accountID
}

func TestServer_SwitchAccount(T *testing.T) {
	T.Parallel()

	// The round trip, with nobody on the request: the refresh token is the
	// whole of its authority.
	T.Run("moves the login to the named account", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)
		second := h.secondAccount(t, true)

		first, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		must.NoError(t, err)
		must.EqOp(t, h.accountID, first.GetToken().GetActiveAccountId())

		switched, err := h.client.SwitchAccount(h.rootCtx, &signinpb.SwitchAccountRequest{
			RefreshToken: first.GetToken().GetRefreshToken(),
			AccountId:    second,
		})
		must.NoError(t, err)

		test.EqOp(t, second, switched.GetToken().GetActiveAccountId())
		test.EqOp(t, first.GetToken().GetFamilyId(), switched.GetToken().GetFamilyId())
		test.NotEqOp(t, first.GetToken().GetRefreshToken(), switched.GetToken().GetRefreshToken())
	})

	// Somebody else's account is answered as a dead token is, on every channel
	// a client reads, and the token survives it.
	T.Run("an account the caller is not in is refused as a dead token is", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)
		foreign := h.secondAccount(t, false)

		first, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		must.NoError(t, err)

		_, err = h.client.SwitchAccount(h.rootCtx, &signinpb.SwitchAccountRequest{
			RefreshToken: first.GetToken().GetRefreshToken(),
			AccountId:    foreign,
		})
		must.Error(t, err)

		_, deadErr := h.client.SwitchAccount(h.rootCtx, &signinpb.SwitchAccountRequest{
			RefreshToken: "a-token-this-service-never-minted",
			AccountId:    h.accountID,
		})
		must.Error(t, deadErr)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
		test.EqOp(t, status.Code(deadErr), status.Code(err))
		test.EqOp(t, status.Convert(deadErr).Message(), status.Convert(err).Message())
		test.EqOp(t, signin.ErrInvalidCredentials.Error(), status.Convert(err).Message())

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: first.GetToken().GetRefreshToken(),
		})
		test.NoError(t, err, test.Sprint("a refused switch spent the token"))
	})

	T.Run("an empty account is a bad request", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		first, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		must.NoError(t, err)

		_, err = h.client.SwitchAccount(h.rootCtx, &signinpb.SwitchAccountRequest{
			RefreshToken: first.GetToken().GetRefreshToken(),
		})
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("is anonymous", func(t *testing.T) {
		t.Parallel()

		test.SliceContains(t, signingrpc.AnonymousMethods(), signinpb.SignInService_SwitchAccount_FullMethodName)
	})
}
