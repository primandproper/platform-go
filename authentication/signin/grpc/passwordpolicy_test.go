package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A mounted server applies the consumer's password policy on every RPC that
// writes a password, because nothing runs between the wire and the write for
// anybody else to apply it. Each refusal is InvalidArgument carrying
// PASSWORD_REFUSED, and each costs the caller nothing: the same request with
// another password goes through.

// minimumLengthPolicy refuses anything shorter than twelve characters.
func minimumLengthPolicy(_ context.Context, password string) error {
	if len(password) < 12 {
		return platformerrors.New("use at least twelve characters")
	}

	return nil
}

// requirePasswordRefused asserts the refusal a client sees on the wire.
func requirePasswordRefused(t *testing.T, err error) {
	t.Helper()

	test.ErrorIs(t, err, signin.ErrPasswordRefused)
	test.EqOp(t, codes.InvalidArgument, status.Code(err))

	info, ok := grpcerrors.ClientReasonFromStatus(err)
	must.True(t, ok)
	test.EqOp(t, "PASSWORD_REFUSED", info.GetReason())
	test.EqOp(t, signin.ClientReasonDomain, info.GetDomain())
}

func TestPasswordPolicy_overTheWire(T *testing.T) {
	T.Parallel()

	T.Run("register", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, []signin.ServiceOption{signin.WithPasswordPolicy(minimumLengthPolicy)})

		request := registrationInput("ada")
		request.Credential = &signinpb.RegisterRequest_Password{Password: "short"}

		_, err := h.client.Register(asUser(h.rootCtx, h.user.ID), request)
		requirePasswordRefused(t, err)

		request.Credential = &signinpb.RegisterRequest_Password{Password: "long enough to pass"}

		_, err = h.client.Register(asUser(h.rootCtx, h.user.ID), request)
		must.NoError(t, err)
	})

	T.Run("update password", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, []signin.ServiceOption{signin.WithPasswordPolicy(minimumLengthPolicy)})

		_, err := h.client.UpdatePassword(h.asJane(), &signinpb.UpdatePasswordRequest{
			CurrentPassword: h.password,
			NewPassword:     "short",
		})
		requirePasswordRefused(t, err)

		// The old password still signs in.
		_, err = h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
		})
		must.NoError(t, err)

		_, err = h.client.UpdatePassword(h.asJane(), &signinpb.UpdatePasswordRequest{
			CurrentPassword: h.password,
			NewPassword:     "long enough to pass",
		})
		must.NoError(t, err)
	})

	T.Run("attach password", func(t *testing.T) {
		t.Parallel()

		secrets := &mailedSecrets{secret: "the-token-in-their-inbox"}
		h := newHarness(t, []signin.ServiceOption{
			signin.WithSecretGenerator(secrets),
			signin.WithPasswordPolicy(minimumLengthPolicy),
		})

		request := registrationInput("ada")
		request.Credential = &signinpb.RegisterRequest_NoPassword{NoPassword: &signinpb.NoPassword{}}

		_, err := h.client.Register(asUser(h.rootCtx, h.user.ID), request)
		must.NoError(t, err)

		_, err = h.client.AttachPassword(h.rootCtx, &signinpb.AttachPasswordRequest{
			Token:       secrets.secret,
			NewPassword: "short",
		})
		requirePasswordRefused(t, err)

		// The link is still live.
		_, err = h.client.AttachPassword(h.rootCtx, &signinpb.AttachPasswordRequest{
			Token:       secrets.secret,
			NewPassword: "long enough to pass",
		})
		must.NoError(t, err)
	})
}
