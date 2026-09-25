package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	passwordresetgrpc "github.com/primandproper/platform-go/v14/authentication/passwordreset/grpc"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestNewServer(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil service", func(t *testing.T) {
		t.Parallel()

		_, err := passwordresetgrpc.NewServer(nil)
		test.ErrorIs(t, err, passwordresetgrpc.ErrNilService)
	})
}

// The whole point of the surface: somebody who cannot sign in gets back in.
func TestPasswordReset_EndToEnd(T *testing.T) {
	T.Parallel()

	T.Run("request, verify, complete", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		before := h.storedPassword(t)

		_, err := h.client.RequestPasswordReset(h.rootCtx, &passwordresetpb.RequestPasswordResetRequest{
			EmailAddress: "jane@example.com",
		})
		must.NoError(t, err)

		// The secret never crosses a response. A test reads it where the person
		// it is about would: out of the mail.
		secret := h.mailer.lastSecret(t)

		verified, err := h.client.VerifyPasswordResetToken(h.rootCtx, &passwordresetpb.VerifyPasswordResetTokenRequest{
			Token: secret,
		})
		must.NoError(t, err)
		must.NotNil(t, verified.GetExpiresAt())

		_, err = h.client.CompletePasswordReset(h.rootCtx, &passwordresetpb.CompletePasswordResetRequest{
			Token:       secret,
			NewPassword: "a whole new password",
		})
		must.NoError(t, err)

		test.NotEqOp(t, before, h.storedPassword(t))
	})

	// Single use, over the wire. The link that just worked is spent, and the
	// refusal says which of the three happened because whoever is asking already
	// holds the secret.
	T.Run("a spent link cannot be spent again", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.client.RequestPasswordReset(h.rootCtx, &passwordresetpb.RequestPasswordResetRequest{
			EmailAddress: "jane@example.com",
		})
		must.NoError(t, err)

		secret := h.mailer.lastSecret(t)

		_, err = h.client.CompletePasswordReset(h.rootCtx, &passwordresetpb.CompletePasswordResetRequest{
			Token:       secret,
			NewPassword: "a whole new password",
		})
		must.NoError(t, err)

		_, err = h.client.VerifyPasswordResetToken(h.rootCtx, &passwordresetpb.VerifyPasswordResetTokenRequest{
			Token: secret,
		})
		test.ErrorIs(t, err, passwordreset.ErrTokenRedeemed)

		_, err = h.client.CompletePasswordReset(h.rootCtx, &passwordresetpb.CompletePasswordResetRequest{
			Token:       secret,
			NewPassword: "another password entirely",
		})
		test.ErrorIs(t, err, passwordreset.ErrTokenRedeemed)
	})

	// Completing a reset withdraws every other link that person was holding, so
	// a second request made before the first was answered does not stay live
	// afterwards.
	T.Run("completing one link withdraws the others", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		for range 2 {
			_, err := h.client.RequestPasswordReset(h.rootCtx, &passwordresetpb.RequestPasswordResetRequest{
				EmailAddress: "jane@example.com",
			})
			must.NoError(t, err)
		}

		must.EqOp(t, 2, h.mailer.count())

		first := h.mailer.sent[0].Issuance.Secret
		second := h.mailer.lastSecret(t)
		must.StrNotEqFold(t, first, second)

		_, err := h.client.CompletePasswordReset(h.rootCtx, &passwordresetpb.CompletePasswordResetRequest{
			Token:       second,
			NewPassword: "a whole new password",
		})
		must.NoError(t, err)

		_, err = h.client.VerifyPasswordResetToken(h.rootCtx, &passwordresetpb.VerifyPasswordResetTokenRequest{
			Token: first,
		})
		test.Error(t, err)
	})
}

// The silence the surface owes. This is the assertion that would fail if
// somebody made the response say anything.
func TestRequestPasswordReset_SaysNothingAboutWhoExists(T *testing.T) {
	T.Parallel()

	T.Run("an unknown address is answered exactly like a known one", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		known, err := h.client.RequestPasswordReset(h.rootCtx, &passwordresetpb.RequestPasswordResetRequest{
			EmailAddress: "jane@example.com",
		})
		must.NoError(t, err)

		unknown, err := h.client.RequestPasswordReset(h.rootCtx, &passwordresetpb.RequestPasswordResetRequest{
			EmailAddress: "nobody@example.com",
		})
		must.NoError(t, err)

		// Identical on the wire, which is what the empty message is for.
		test.True(t, proto.Equal(known, unknown))

		// And only one of them sent mail, which is the half a response that said
		// more would have leaked.
		test.EqOp(t, 1, h.mailer.count())
	})

	// An empty address is the calling code being wrong rather than a guess about
	// who exists, so it is refused rather than padded and swallowed.
	T.Run("an empty address is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.client.RequestPasswordReset(h.rootCtx, &passwordresetpb.RequestPasswordResetRequest{})
		test.ErrorIs(t, err, passwordreset.ErrEmptyEmailAddress)
	})
}

func TestPasswordReset_Refusals(T *testing.T) {
	T.Parallel()

	T.Run("a token nobody was issued is told apart from one that was", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.client.VerifyPasswordResetToken(h.rootCtx, &passwordresetpb.VerifyPasswordResetTokenRequest{
			Token: "a-token-this-service-never-minted",
		})
		test.ErrorIs(t, err, passwordreset.ErrTokenNotFound)
	})

	// The rule the service applies before it spends anything: somebody who
	// submitted an empty form still holds their link.
	T.Run("an empty password is refused and the link survives it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.client.RequestPasswordReset(h.rootCtx, &passwordresetpb.RequestPasswordResetRequest{
			EmailAddress: "jane@example.com",
		})
		must.NoError(t, err)

		secret := h.mailer.lastSecret(t)

		_, err = h.client.CompletePasswordReset(h.rootCtx, &passwordresetpb.CompletePasswordResetRequest{
			Token: secret,
		})
		test.ErrorIs(t, err, passwordreset.ErrEmptyNewPassword)

		_, err = h.client.VerifyPasswordResetToken(h.rootCtx, &passwordresetpb.VerifyPasswordResetTokenRequest{
			Token: secret,
		})
		test.NoError(t, err)
	})
}

// The one seam, and the one failure it has.
func TestScopeResolver(T *testing.T) {
	T.Parallel()

	T.Run("a request that cannot be placed is refused rather than answered globally", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, passwordresetgrpc.WithScopeResolver(
			func(context.Context) (tenancy.Scope, error) {
				return tenancy.Global(), platformerrors.ErrUnrecognizedInputValue
			},
		))

		_, err := h.client.RequestPasswordReset(h.rootCtx, &passwordresetpb.RequestPasswordResetRequest{
			EmailAddress: "jane@example.com",
		})
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		// Nothing was mailed, which is the point of refusing rather than
		// resolving to the global directory and finding nobody in it.
		test.EqOp(t, 0, h.mailer.count())
	})

	T.Run("a nil resolver leaves the default", func(t *testing.T) {
		t.Parallel()

		srv, err := passwordresetgrpc.NewServer(&passwordreset.Service{}, passwordresetgrpc.WithScopeResolver(nil))
		must.NoError(t, err)
		must.NotNil(t, srv)

		scope, err := passwordresetgrpc.GlobalScope(t.Context())
		must.NoError(t, err)
		test.True(t, scope.IsGlobal())
	})
}

// A mounted server applies the consumer's password policy, because nothing runs
// between the wire and the write for anybody else to apply it. The refusal is
// InvalidArgument with an identifier of its own, which is what lets a client
// tell "choose another password" from the three ways a link is dead — and the
// link is still live afterwards.
func TestCompletePasswordReset_passwordPolicy(T *testing.T) {
	T.Parallel()

	h := newHarnessWithService(T, []passwordreset.ServiceOption{
		passwordreset.WithPasswordPolicy(func(_ context.Context, password string) error {
			if len(password) < 12 {
				return platformerrors.New("use at least twelve characters")
			}

			return nil
		}),
	})

	_, err := h.client.RequestPasswordReset(h.rootCtx, &passwordresetpb.RequestPasswordResetRequest{
		EmailAddress: "jane@example.com",
	})
	must.NoError(T, err)

	secret := h.mailer.lastSecret(T)
	before := h.storedPassword(T)

	_, err = h.client.CompletePasswordReset(h.rootCtx, &passwordresetpb.CompletePasswordResetRequest{
		Token:       secret,
		NewPassword: "short",
	})
	test.ErrorIs(T, err, passwordreset.ErrPasswordRefused)
	test.EqOp(T, codes.InvalidArgument, status.Code(err))

	info, ok := grpcerrors.ClientReasonFromStatus(err)
	must.True(T, ok)
	test.EqOp(T, "REPLACEMENT_PASSWORD_REFUSED", info.GetReason())
	test.EqOp(T, passwordreset.ClientReasonDomain, info.GetDomain())

	test.EqOp(T, before, h.storedPassword(T))

	_, err = h.client.CompletePasswordReset(h.rootCtx, &passwordresetpb.CompletePasswordResetRequest{
		Token:       secret,
		NewPassword: "long enough to pass",
	})
	must.NoError(T, err)
	test.NotEqOp(T, before, h.storedPassword(T))
}
