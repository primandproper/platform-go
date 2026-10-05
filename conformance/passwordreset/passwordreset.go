package passwordreset

import (
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "passwordreset"

// The doors this suite knocks on, as the names a deployment would reserve them
// by. Every one is made with nobody on it, since the person on the other end has
// lost the password they would have signed in with.
const (
	completeReset = passwordresetpb.PasswordResetService_CompletePasswordReset_FullMethodName
	requestReset  = passwordresetpb.PasswordResetService_RequestPasswordReset_FullMethodName
	verifyReset   = passwordresetpb.PasswordResetService_VerifyPasswordResetToken_FullMethodName
)

// Suite is the password reset surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
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
//
// doors are the calls the test goes on to make with nobody on them, and it
// skips where the subject reserves any of them.
func resettable(t *testing.T, s *conformance.Session, doors ...string) (*conformance.Subject, *identitypb.User) {
	t.Helper()

	s.NeedsPublic(t, doors...)

	sub := s.Subject(t,
		conformance.Making(append([]string{identitypb.IdentityService_GetPrincipal_FullMethodName}, doors...)...),
		conformance.InTenant(surface, tenancy.Global()))

	if sub.Surfaces.Identity == nil {
		conformance.Skip(t, "conformance: this subject mounts no identity surface, so a caller's address cannot be read")
	}

	found, err := sub.Surfaces.Identity.GetPrincipal(sub.Context(t.Context()), &identitypb.GetPrincipalRequest{})
	must.NoError(t, err, must.Sprint("a caller could not read its own principal"))
	must.StrNotEqFold(t, "", found.GetPrincipal().GetUser().GetEmailAddress(), must.Sprint("the caller has no address to reset through"))

	return sub, found.GetPrincipal().GetUser()
}

// doors mints a caller whose connection the doors are knocked on through, with
// nobody on the call, skipping where the subject reserves any of them.
func doors(t *testing.T, s *conformance.Session, calls ...string) *conformance.Subject {
	t.Helper()

	s.NeedsPublic(t, calls...)

	return s.Subject(t, conformance.Making(calls...))
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

	secret, err := read(t.Context(), sub.ScopeFor(surface), emailAddress)
	must.NoError(t, err, must.Sprint("reading the reset link the deployment mailed"))
	must.StrNotEqFold(t, "", secret, must.Sprint("the deployment mailed an empty secret"))

	return secret
}
