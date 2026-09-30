package signin

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// The operator's calls on somebody else's logins.
const (
	listSignInsForUser   = signinpb.SignInAdministrationService_ListSignInsForUser_FullMethodName
	endSignInForUser     = signinpb.SignInAdministrationService_EndSignInForUser_FullMethodName
	endAllSignInsForUser = signinpb.SignInAdministrationService_EndAllSignInsForUser_FullMethodName
)

// operator is a caller in the global directory — the one a registrant is in —
// making the administrative calls, which is an operator where the subject
// reserves them and a member where it does not. It skips where the subject
// mounts no administrative surface.
//
// That a member is refused them is not asserted here. It is the reservations
// suite's, which holds every call the subject names in Seams.OperatorMethods
// to a refusal, and a deployment that grants these to nobody but its staff
// names them there.
func operator(t *testing.T, s *conformance.Session, methods ...string) *conformance.Subject {
	t.Helper()

	sub := s.Subject(t, conformance.Making(methods...), conformance.InTenant(surface, tenancy.Global()))
	if sub.Surfaces.SignInAdministration == nil {
		conformance.Skip(t, "conformance: this subject mounts no sign-in administration surface, so nobody can act on another person's logins; skipping")
	}

	return sub
}

func administration(t *testing.T, s *conformance.Session) {
	t.Helper()

	// The case the surface exists for: an operator reads a person's logins
	// off the list, ends the one they were asked about, and that login's
	// refresh is refused while the person's other login goes on working.
	t.Run("an operator ends one of a member's logins, and the member's refresh is refused", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, exchangeRefreshToken)
		who := signInAs(t, s, anon)

		phone := rotating(t, s, anon, who.username, password)
		laptop := rotating(t, s, anon, who.username, password)
		must.NotEqOp(t, phone.GetFamilyId(), laptop.GetFamilyId(), must.Sprint("two sign-ins were one login"))

		op := operator(t, s, listSignInsForUser, endSignInForUser)
		ctx := op.Context(t.Context())

		listed, err := op.Surfaces.SignInAdministration.ListSignInsForUser(ctx,
			&signinpb.ListSignInsForUserRequest{UserId: who.userID})
		must.NoError(t, err, must.Sprint("an operator could not list a member's logins"))

		families := map[string]bool{}
		for _, signIn := range listed.GetSignIns() {
			families[signIn.GetFamilyId()] = true

			test.False(t, signIn.GetCurrent(), test.Sprint("a member's login is marked as the one the operator asked through"))
		}

		test.MapContainsKey(t, families, phone.GetFamilyId())
		test.MapContainsKey(t, families, laptop.GetFamilyId())

		_, err = op.Surfaces.SignInAdministration.EndSignInForUser(ctx, &signinpb.EndSignInForUserRequest{
			UserId:   who.userID,
			FamilyId: phone.GetFamilyId(),
		})
		must.NoError(t, err, must.Sprint("an operator could not end a member's login"))

		_, err = exchange(t.Context(), anon, phone.GetRefreshToken())
		refused(t, s, err, codes.Unauthenticated, reasonInvalidCredentials)

		// The control, and the scope of the end: the laptop is another login.
		_, err = exchange(t.Context(), anon, laptop.GetRefreshToken())
		test.NoError(t, err, test.Sprint("ending one of a member's logins ended another"))
	})

	// The person is part of the key, so an operator who names a login
	// against the wrong person ends nobody's.
	t.Run("an operator naming a login against somebody else ends nothing", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, exchangeRefreshToken)
		owner := signInAs(t, s, anon)
		stranger := signInAs(t, s, anon)

		theirs := rotating(t, s, anon, owner.username, password)

		op := operator(t, s, endSignInForUser)

		_, err := op.Surfaces.SignInAdministration.EndSignInForUser(op.Context(t.Context()), &signinpb.EndSignInForUserRequest{
			UserId:   stranger.userID,
			FamilyId: theirs.GetFamilyId(),
		})
		must.NoError(t, err)

		_, err = exchange(t.Context(), anon, theirs.GetRefreshToken())
		test.NoError(t, err, test.Sprint("a login was ended by naming it against somebody who does not hold it"))
	})

	t.Run("an operator ends every login a member holds", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, exchangeRefreshToken)
		who := signInAs(t, s, anon)
		bystander := signInAs(t, s, anon)

		phone := rotating(t, s, anon, who.username, password)
		laptop := rotating(t, s, anon, who.username, password)
		theirs := rotating(t, s, anon, bystander.username, password)

		op := operator(t, s, endAllSignInsForUser)

		_, err := op.Surfaces.SignInAdministration.EndAllSignInsForUser(op.Context(t.Context()),
			&signinpb.EndAllSignInsForUserRequest{UserId: who.userID})
		must.NoError(t, err, must.Sprint("an operator could not end a member's logins"))

		for _, ended := range []*signinpb.IssuedToken{phone, laptop} {
			_, err = exchange(t.Context(), anon, ended.GetRefreshToken())
			refused(t, s, err, codes.Unauthenticated, reasonInvalidCredentials)
		}

		// The control: somebody else's login is not the member's.
		_, err = exchange(t.Context(), anon, theirs.GetRefreshToken())
		test.NoError(t, err, test.Sprint("ending a member's logins ended somebody else's"))
	})
}
