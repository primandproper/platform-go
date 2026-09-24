package passwordreset

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func redemption(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a mailed link is verified, then redeemed", func(t *testing.T) {
		t.Parallel()

		sub, user := resettable(t, s)
		request(t, sub, user.GetEmailAddress())
		secret := mailed(t, s, sub, user.GetEmailAddress())

		verified, err := sub.Surfaces.PasswordReset.VerifyPasswordResetToken(t.Context(),
			&passwordresetpb.VerifyPasswordResetTokenRequest{Token: secret})
		must.NoError(t, err, must.Sprint("a link the deployment mailed did not verify"))
		must.NotNil(t, verified.GetExpiresAt(), must.Sprint("a verified link reported no deadline to render"))

		// Whole seconds, and generously: what is asserted is that the deadline
		// is ahead of now rather than how far, which is the deployment's.
		test.True(t, verified.GetExpiresAt().AsTime().After(time.Now().Add(-time.Second)),
			test.Sprint("a link that verified reported a deadline already past"))

		_, err = sub.Surfaces.PasswordReset.CompletePasswordReset(t.Context(),
			&passwordresetpb.CompletePasswordResetRequest{Token: secret, NewPassword: newPassword})
		must.NoError(t, err)
	})

	// The whole point of the surface, asserted where it pays off: somebody who
	// could not sign in now can, with the password the link set. It needs the
	// sign-in surface to observe, and a subject that did not mount one skips.
	t.Run("the password a reset sets is the one that signs in", func(t *testing.T) {
		t.Parallel()

		sub, user := resettable(t, s)

		if sub.Surfaces.SignIn == nil {
			t.Skip("conformance: this subject mounts no sign-in surface, so a reset's effect cannot be observed")
		}

		signIn := func(password string) error {
			_, err := sub.Surfaces.SignIn.LoginForToken(t.Context(), &signinpb.LoginForTokenRequest{
				Credentials: &signinpb.Credentials{Username: user.GetUsername(), Password: password},
			})

			return err
		}

		// The control: before the reset this password is not theirs, so a
		// sign-in that succeeded below would otherwise prove nothing.
		must.Error(t, signIn(newPassword), must.Sprint("the caller signed in with a password nobody set"))

		request(t, sub, user.GetEmailAddress())

		_, err := sub.Surfaces.PasswordReset.CompletePasswordReset(t.Context(),
			&passwordresetpb.CompletePasswordResetRequest{
				Token:       mailed(t, s, sub, user.GetEmailAddress()),
				NewPassword: newPassword,
			})
		must.NoError(t, err)

		test.NoError(t, signIn(newPassword), test.Sprint("the password a reset set does not sign in"))
	})

	// Single use, and the refusal says which of the three ways a link fails,
	// because whoever is asking already holds the secret.
	t.Run("a spent link cannot be spent again, and says so", func(t *testing.T) {
		t.Parallel()

		sub, user := resettable(t, s)
		request(t, sub, user.GetEmailAddress())
		secret := mailed(t, s, sub, user.GetEmailAddress())

		_, err := sub.Surfaces.PasswordReset.CompletePasswordReset(t.Context(),
			&passwordresetpb.CompletePasswordResetRequest{Token: secret, NewPassword: newPassword})
		must.NoError(t, err)

		_, err = sub.Surfaces.PasswordReset.VerifyPasswordResetToken(t.Context(),
			&passwordresetpb.VerifyPasswordResetTokenRequest{Token: secret})
		must.Error(t, err, must.Sprint("a spent link still verified"))
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.StrContains(t, status.Convert(err).Message(), "redeemed")

		_, err = sub.Surfaces.PasswordReset.CompletePasswordReset(t.Context(),
			&passwordresetpb.CompletePasswordResetRequest{Token: secret, NewPassword: "another password entirely"})
		must.Error(t, err, must.Sprint("a spent link was spent again"))
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.StrContains(t, status.Convert(err).Message(), "redeemed")
	})

	// A second request made before the first was answered must not stay live
	// once either has been used.
	t.Run("redeeming one link withdraws the others", func(t *testing.T) {
		t.Parallel()

		sub, user := resettable(t, s)

		request(t, sub, user.GetEmailAddress())
		first := mailed(t, s, sub, user.GetEmailAddress())

		request(t, sub, user.GetEmailAddress())
		second := mailed(t, s, sub, user.GetEmailAddress())
		must.NotEqOp(t, first, second, must.Sprint("two requests were mailed one link"))

		// The control: the first is live before the second is redeemed.
		_, err := sub.Surfaces.PasswordReset.VerifyPasswordResetToken(t.Context(),
			&passwordresetpb.VerifyPasswordResetTokenRequest{Token: first})
		must.NoError(t, err, must.Sprint("the first link was dead before anything was redeemed"))

		_, err = sub.Surfaces.PasswordReset.CompletePasswordReset(t.Context(),
			&passwordresetpb.CompletePasswordResetRequest{Token: second, NewPassword: newPassword})
		must.NoError(t, err)

		_, err = sub.Surfaces.PasswordReset.VerifyPasswordResetToken(t.Context(),
			&passwordresetpb.VerifyPasswordResetTokenRequest{Token: first})
		must.Error(t, err, must.Sprint("an earlier link survived a later one being redeemed"))
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})
}
