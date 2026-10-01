package passwordreset

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func redemption(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a mailed link is verified, then redeemed", func(t *testing.T) {
		t.Parallel()

		sub, user := resettable(t, s, requestReset, verifyReset, completeReset)
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
	//
	// A caller the subject minted is the deployment's, and may hold a proven
	// second factor the suite has no code for. Their principal says whether
	// they do, and where they do the door's answer to the password alone is
	// that a code is required — the one refusal sign-in gives only to a
	// password that was right, so it is what the reset is held to there.
	t.Run("the password a reset sets is the one that signs in", func(t *testing.T) {
		t.Parallel()

		sub, user := resettable(t, s, requestReset, completeReset, signinpb.SignInService_LoginForToken_FullMethodName)

		if sub.Surfaces.SignIn == nil {
			conformance.Skip(t, "conformance: this subject mounts no sign-in surface, so a reset's effect cannot be observed")
		}

		holdsSecondFactor := user.GetTwoFactorSecretVerifiedAt() != nil

		signIn := func(password string) error {
			_, err := sub.Surfaces.SignIn.LoginForToken(t.Context(), &signinpb.LoginForTokenRequest{
				Credentials: &signinpb.Credentials{Username: user.GetUsername(), Password: password},
			})

			return err
		}

		// The control: before the reset this password is not theirs, so a
		// sign-in that succeeded below would otherwise prove nothing — and
		// for somebody holding a second factor, neither would being asked for
		// a code.
		before := signIn(newPassword)
		must.Error(t, before, must.Sprint("the caller signed in with a password nobody set"))

		if holdsSecondFactor && reasons(t, s) {
			must.NotEqOp(t, reasonSecondFactorRequired, reason(before),
				must.Sprint("a password nobody set was answered as a right one"))
		}

		request(t, sub, user.GetEmailAddress())

		_, err := sub.Surfaces.PasswordReset.CompletePasswordReset(t.Context(),
			&passwordresetpb.CompletePasswordResetRequest{
				Token:       mailed(t, s, sub, user.GetEmailAddress()),
				NewPassword: newPassword,
			})
		must.NoError(t, err)

		after := signIn(newPassword)
		if !holdsSecondFactor {
			test.NoError(t, after, test.Sprint("the password a reset set does not sign in"))

			return
		}

		must.Error(t, after, must.Sprint("somebody holding a proven second factor signed in with no code"))
		test.EqOp(t, codes.Unauthenticated, status.Code(after))

		if reasons(t, s) {
			test.EqOp(t, reasonSecondFactorRequired, reason(after),
				test.Sprint("the password a reset set was not answered as the right one"))
		}
	})

	// Single use, and the refusal says which of the three ways a link fails,
	// because whoever is asking already holds the secret.
	t.Run("a spent link cannot be spent again, and says so", func(t *testing.T) {
		t.Parallel()

		sub, user := resettable(t, s, requestReset, completeReset, verifyReset)
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

		sub, user := resettable(t, s, requestReset, verifyReset, completeReset)

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

// reasonSecondFactorRequired is the reason sign-in's contract gives a right
// password sent without the code its holder's second factor asks for.
const reasonSecondFactorRequired = "SECOND_FACTOR_REQUIRED"

// reason is the client-safe reason a sign-in refusal carried in signin's
// domain, or empty where it carried none. It is the reason rather than the
// message that a reset is held to, because the reason is what sign-in's
// contract tells a client to branch on, and the message is prose a deployment
// may reword.
func reason(err error) string {
	info, ok := grpcerrors.ClientReasonFromStatus(err)
	if !ok || info.GetDomain() != signin.ClientReasonDomain {
		return ""
	}

	return info.GetReason()
}

// reasons reports whether s's subject carries reasons to its clients, printing
// what goes unasserted where it does not. Without one, a wrong password and a
// right one awaiting its code are the same code, so only the code is held.
func reasons(t *testing.T, s *conformance.Session) bool {
	t.Helper()

	if !s.Seams().ErrorReasonsStripped {
		return true
	}

	t.Log("conformance: this subject says its edge strips client-safe reasons (Seams.ErrorReasonsStripped), so whether a refusal was a wrong password or a missing code is not asserted")

	return false
}
