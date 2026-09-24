package passwordreset

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// Suite is the password reset surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    "passwordreset",
		Mounted: func(s conformance.Surfaces) bool { return s.PasswordReset != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("redemption", func(t *testing.T) {
		t.Parallel()
		redemption(t, s)
	})
	t.Run("refusals", func(t *testing.T) {
		t.Parallel()
		refusals(t, s)
	})
}

// newPassword is what a reset sets. Long enough that no deployment's strength
// rule refuses it, since the assertions are about the link rather than the
// password.
const newPassword = "a whole new password, long enough for anybody"

// resettable mints a caller in the directory a reset request is placed in,
// and reads the address a reset is requested for. See the package
// documentation for why that directory is the global one.
func resettable(t *testing.T, s *conformance.Session) (*conformance.Subject, *identitypb.User) {
	t.Helper()

	sub := s.Subject(t, conformance.InTenant(tenancy.Global()))

	if sub.Surfaces.Identity == nil {
		t.Skip("conformance: this subject mounts no identity surface, so a caller's address cannot be read")
	}

	found, err := sub.Surfaces.Identity.GetUser(sub.Context(t.Context()),
		&identitypb.GetUserRequest{UserId: sub.UserID})
	must.NoError(t, err, must.Sprint("a caller could not read its own user"))
	must.StrNotEqFold(t, "", found.GetUser().GetEmailAddress(), must.Sprint("the caller has no address to reset through"))

	return sub, found.GetUser()
}

// request asks for a reset link for an address, as the form nobody has signed
// in to does.
func request(t *testing.T, sub *conformance.Subject, emailAddress string) *passwordresetpb.RequestPasswordResetResponse {
	t.Helper()

	response, err := sub.Surfaces.PasswordReset.RequestPasswordReset(t.Context(),
		&passwordresetpb.RequestPasswordResetRequest{EmailAddress: emailAddress})
	must.NoError(t, err, must.Sprint("requesting a reset link"))

	return response
}

// mailed is the secret the deployment most recently mailed to an address, or a
// skip where the subject cannot say.
func mailed(t *testing.T, s *conformance.Session, sub *conformance.Subject, emailAddress string) string {
	t.Helper()

	read := s.Seams().Actions.PasswordResetToken
	s.NeedsAction(t, read != nil, "password reset token")

	secret, err := read(t.Context(), sub.Scope, emailAddress)
	must.NoError(t, err, must.Sprint("reading the reset link the deployment mailed"))
	must.StrNotEqFold(t, "", secret, must.Sprint("the deployment mailed an empty secret"))

	return secret
}
