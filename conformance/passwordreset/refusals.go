package passwordreset

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func refusals(t *testing.T, s *conformance.Session) {
	t.Helper()

	// The silence the surface owes, and the assertion that fails if somebody
	// makes the response say anything.
	t.Run("an unknown address is answered exactly as a known one", func(t *testing.T) {
		t.Parallel()

		sub, user := resettable(t, s)

		known := request(t, sub, user.GetEmailAddress())

		// The control: the known address really was a known one, and was sent
		// a link. Two identical answers from a deployment that mails nobody
		// would prove nothing.
		mailed(t, s, sub, user.GetEmailAddress())

		unknown := request(t, sub, identifiers.New()+"@conformance.invalid")

		test.True(t, proto.Equal(known, unknown),
			test.Sprint("a reset request told a known address apart from an unknown one"))
	})

	// The calling code being wrong rather than a guess about who exists, so it
	// is refused rather than padded and swallowed.
	t.Run("an empty address is refused", func(t *testing.T) {
		t.Parallel()

		sub := s.Subject(t)

		_, err := sub.Surfaces.PasswordReset.RequestPasswordReset(t.Context(),
			&passwordresetpb.RequestPasswordResetRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("a link nobody was issued is told apart from one that was", func(t *testing.T) {
		t.Parallel()

		sub := s.Subject(t)

		_, err := sub.Surfaces.PasswordReset.VerifyPasswordResetToken(t.Context(),
			&passwordresetpb.VerifyPasswordResetTokenRequest{Token: identifiers.New()})
		must.Error(t, err)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))

		// The sentence is the point: all three ways a link fails share the
		// code, and the person holding one is owed which.
		test.StrContains(t, status.Convert(err).Message(), "not found")
	})

	// The rule applied before anything is spent: somebody who submitted an
	// empty form still holds their link.
	t.Run("an empty password is refused and the link survives it", func(t *testing.T) {
		t.Parallel()

		sub, user := resettable(t, s)
		request(t, sub, user.GetEmailAddress())
		secret := mailed(t, s, sub, user.GetEmailAddress())

		_, err := sub.Surfaces.PasswordReset.CompletePasswordReset(t.Context(),
			&passwordresetpb.CompletePasswordResetRequest{Token: secret})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		_, err = sub.Surfaces.PasswordReset.VerifyPasswordResetToken(t.Context(),
			&passwordresetpb.VerifyPasswordResetTokenRequest{Token: secret})
		test.NoError(t, err, test.Sprint("a refused completion spent the link anyway"))
	})
}
