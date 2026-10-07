package passkeys

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/passkeys/passkeyspb"
	domain "github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn/webauthntest"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The administrative door, and the call that makes somebody an operator it
// admits.
const (
	adminFinishLogin    = passkeyspb.PasskeysService_AdminFinishLogin_FullMethodName
	setUserServiceRoles = identitypb.IdentityService_SetUserServiceRoles_FullMethodName
)

// identitySurface is the identity suite's name, for the operator here whose
// call is identity's: the tenant it is asked into is read on that surface.
const identitySurface = "identity"

// The reasons docs/client-contract.md lists for the administrative door's
// refusals. They are sign-in's, because sign-in is what refuses them.
const (
	reasonMultiFactorRequired    = "MULTI_FACTOR_REQUIRED"
	reasonNotAnAdministrator     = "NOT_AN_ADMINISTRATOR"
	reasonAdminSignInUnavailable = "ADMIN_SIGNIN_UNAVAILABLE"
)

func administrative(t *testing.T, s *conformance.Session) {
	t.Helper()

	// The reason the door exists: an operator's passkey signs them in to a
	// token that carries the administrative claim, in both of a login's forms.
	// The control is the same person before the grant, refused on the role
	// after their assertion was proven — the move from that refusal to the
	// token shows the grant took, and that the door is there to refuse on it.
	t.Run("an operator's verified passkey signs them in through the administrative door", func(t *testing.T) {
		t.Parallel()

		sub := enrollee(t, s, getAuthStatus)
		key := enroll(t, s, sub)
		anon := doors(t, s, adminFinishLogin)

		username := usernameOf(t, sub)
		role := administratorRole(t, s)

		// Naming Roles.Administrator is the subject declaring it has the
		// door, so the refusal before the grant is on the role and never on
		// the door being absent.
		_, err := adminFinish(t, anon, username, assertion(t, anon, username, key.device, nil))
		refused(t, s, err, codes.PermissionDenied, reasonNotAnAdministrator)

		promote(t, s, sub, role)

		named, err := adminFinish(t, anon, username, assertion(t, anon, username, key.device, nil))
		must.NoError(t, err, must.Sprint("an operator's verified passkey was refused at the administrative door, by name"))
		test.NotEqOp(t, "", named.GetToken(), test.Sprint("an administrative passkey login answered with no token"))
		test.True(t, named.GetAdministrative(), test.Sprint("the administrative door minted an ordinary token, by name"))

		discovered, err := adminFinish(t, anon, "", assertion(t, anon, "", key.device, key.handle))
		must.NoError(t, err, must.Sprint("an operator's verified passkey was refused at the administrative door, discoverably"))
		test.NotEqOp(t, "", discovered.GetToken(), test.Sprint("an administrative passkey login answered with no token"))
		test.True(t, discovered.GetAdministrative(), test.Sprint("the administrative door minted an ordinary token, discoverably"))
	})

	// The administrative claim is the door's and never the person's: the
	// same operator through the ordinary door is signed in, to a token that
	// does not carry it.
	t.Run("the ordinary door mints an operator an ordinary token", func(t *testing.T) {
		t.Parallel()

		key := enroll(t, s, operatorEnrollee(t, s))
		anon := doors(t, s)

		issued := signIn(t, anon, key)
		test.False(t, issued.GetAdministrative(), test.Sprint("the ordinary door minted an administrative token for an operator"))
	})

	// The administrative door takes no code, so a credential has to have been
	// two factors on its own: a key tap that did not verify the person is
	// refused for the credential rather than the person, and no token is
	// issued. The control is the operator's own verified passkey through the
	// same door.
	t.Run("an operator's key tap alone is refused at the administrative door", func(t *testing.T) {
		t.Parallel()

		sub := operatorEnrollee(t, s)
		tap := enrollKeyTap(t, s, sub)
		anon := doors(t, s, adminFinishLogin)

		issued, err := adminFinish(t, anon, "", assertion(t, anon, "", tap.device, tap.handle))
		refused(t, s, err, codes.PermissionDenied, reasonMultiFactorRequired)
		test.Nil(t, issued, test.Sprint("a refused administrative passkey login answered with a token"))

		verified := enroll(t, s, sub)

		issued, err = adminFinish(t, anon, "", assertion(t, anon, "", verified.device, verified.handle))
		must.NoError(t, err, must.Sprint("an operator's verified passkey was refused at the administrative door"))
		test.True(t, issued.GetAdministrative())
	})

	// Whether a deployment has an administrative door at all is its own
	// decision, and both refusals — no door, or not admitted through it —
	// share a code. What every deployment owes is the order: somebody never
	// made an operator learns that only by proving who they are, so an
	// assertion that proves nothing is refused as a bad assertion is at the
	// ordinary door, and the proven one is refused on the role.
	t.Run("the administrative door refuses somebody who is no operator, and only once their assertion is proven", func(t *testing.T) {
		t.Parallel()

		key := enroll(t, s, enrollee(t, s))
		anon := doors(t, s, adminFinishLogin)

		_, err := adminFinish(t, anon, "", key.device.Assert(t, neverIssued(t), key.handle))
		test.EqOp(t, codes.Unauthenticated, status.Code(err),
			test.Sprint("an assertion that proved nothing was answered as something other than a refused login"))

		issued, err := adminFinish(t, anon, "", assertion(t, anon, "", key.device, key.handle))
		must.Error(t, err, must.Sprint("somebody nobody made an operator signed in through the administrative door"))
		test.Nil(t, issued)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		if reasons(t, s) {
			test.True(t, slices.Contains([]string{reasonNotAnAdministrator, reasonAdminSignInUnavailable}, reason(err)),
				test.Sprintf("the administrative refusal carried reason %q", reason(err)))
		}

		// The same passkey through the ordinary door, so the refusal above
		// was about the door rather than the passkey.
		test.False(t, signIn(t, anon, key).GetAdministrative())
	})
}

// administratorRole is the service role the deployment's administrative door
// admits, skipping where the subject names none: which role that is is exactly
// what a door is configured with, and a guess that missed would read as the
// door refusing an operator.
func administratorRole(t *testing.T, s *conformance.Session) string {
	t.Helper()

	role := s.Roles().Administrator
	if role == "" {
		conformance.Skip(t, "conformance: this subject names no Roles.Administrator, so nobody can be made one the administrative door admits; skipping")
	}

	return role
}

// promote makes sub an operator the administrative door admits, as an
// operator in the directory sub is in would. It skips where the subject mounts
// no identity surface to do it through.
func promote(t *testing.T, s *conformance.Session, sub *conformance.Subject, role string) {
	t.Helper()

	granter := s.Subject(t, conformance.Making(setUserServiceRoles), conformance.InTenant(identitySurface, tenancy.Global()))
	if granter.Surfaces.Identity == nil {
		conformance.Skip(t, "conformance: this subject mounts no identity surface, so nobody can be made an operator; skipping")
	}

	_, err := granter.Surfaces.Identity.SetUserServiceRoles(granter.Context(t.Context()),
		&identitypb.SetUserServiceRolesRequest{UserId: sub.UserID, Roles: []string{role}})
	must.NoError(t, err, must.Sprint("making the enrollee an operator"))
}

// operatorEnrollee is an enrollee made an operator the administrative door
// admits.
func operatorEnrollee(t *testing.T, s *conformance.Session) *conformance.Subject {
	t.Helper()

	role := administratorRole(t, s)
	sub := enrollee(t, s)
	promote(t, s, sub, role)

	return sub
}

// enrollKeyTap enrolls, for sub, a device that proves presence and nothing
// more. A deployment whose relying party requires the person be verified
// refuses the enrollment, and then no key tap reaches any door of its: the
// assertion skips, saying so.
func enrollKeyTap(t *testing.T, s *conformance.Session, sub *conformance.Subject) *enrolled {
	t.Helper()

	rp := s.Seams().WebAuthn

	key, err := enrollDevice(t, sub, webauthntest.NewAuthenticator(t, rp.RPID, rp.Origin, webauthntest.WithoutUserVerification()))
	if err != nil {
		conformance.Skipf(t, "conformance: this subject refused to enroll a passkey that does not verify the person (%v), so no key tap reaches its administrative door; skipping", err)
	}

	return key
}

// adminFinish answers a login through the administrative door, with nobody on
// the call.
func adminFinish(t *testing.T, anon *conformance.Subject, username string, response []byte) (*signinpb.IssuedToken, error) {
	t.Helper()

	finished, err := anon.Surfaces.Passkeys.AdminFinishLogin(t.Context(), &passkeyspb.AdminFinishLoginRequest{
		Username: username,
		Response: response,
	})
	if err != nil {
		return nil, err
	}

	return finished.GetToken(), nil
}

// reason is the client-safe reason a refusal carried in sign-in's domain, or
// empty where it carried none. The domain is checked as well as the type,
// because a reason from somebody else's domain is not one a client is told to
// branch on.
func reason(err error) string {
	info, ok := grpcerrors.ClientReasonFromStatus(err)
	if !ok || info.GetDomain() != domain.ClientReasonDomain {
		return ""
	}

	return info.GetReason()
}

// reasons reports whether s's subject carries reasons to its clients, printing
// what goes unasserted where it does not.
func reasons(t *testing.T, s *conformance.Session) bool {
	t.Helper()

	if !s.Seams().ErrorReasonsStripped {
		return true
	}

	t.Log("conformance: this subject says its edge strips client-safe reasons (Seams.ErrorReasonsStripped), so the reason half of this refusal is not asserted")

	return false
}

// refused asserts err is a refusal with the code and reason the contract lists
// for it. The reason is asserted only where the subject carries one.
func refused(t *testing.T, s *conformance.Session, err error, code codes.Code, want string) {
	t.Helper()

	must.Error(t, err, must.Sprintf("expected a refusal answering %s", want))
	test.EqOp(t, code, status.Code(err))

	if reasons(t, s) {
		test.EqOp(t, want, reason(err), test.Sprintf("the refusal carried reason %q (%v)", reason(err), err))
	}
}
