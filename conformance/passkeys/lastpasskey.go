package passkeys

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passkeys/passkeyspb"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The sign-in calls the last-passkey assertion makes to have somebody with no
// password at all: a registration naming none, and the mailed link that signs
// them in.
const (
	signInSurface    = "signin"
	register         = signinpb.SignInService_Register_FullMethodName
	requestMagicLink = signinpb.SignInService_RequestMagicLink_FullMethodName
	redeemMagicLink  = signinpb.SignInService_RedeemMagicLink_FullMethodName
)

func lastPasskey(t *testing.T, s *conformance.Session) {
	t.Helper()

	// The person the guard is for: registered with no password, signed in
	// with nothing but a mailed link, whose only way back in once the link is
	// spent is their passkey. Archiving it is refused, and archiving the first
	// of two is not, which is the control.
	t.Run("the last passkey of somebody with no password stays", func(t *testing.T) {
		t.Parallel()

		person := passwordless(t, s)

		first := enroll(t, s, person)

		_, err := person.Surfaces.Passkeys.ArchivePasskey(person.Context(t.Context()),
			&passkeyspb.ArchivePasskeyRequest{Id: first.passkey.GetId()})
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.EqOp(t, 1, listed(t, person, first.device.CredentialID()))

		enroll(t, s, person)

		_, err = person.Surfaces.Passkeys.ArchivePasskey(person.Context(t.Context()),
			&passkeyspb.ArchivePasskeyRequest{Id: first.passkey.GetId()})
		test.NoError(t, err)
		test.EqOp(t, 0, listed(t, person, first.device.CredentialID()))
	})
}

// passwordless registers somebody naming no password and signs them in on a
// mailed link, answering with a caller making the passkey self-service calls
// as them. It skips where the subject cannot: no sign-in surface, a door
// reserved, or no way to read the link it mailed.
func passwordless(t *testing.T, s *conformance.Session) *conformance.Subject {
	t.Helper()

	read := s.Seams().Actions.MagicLinkToken
	s.NeedsAction(t, read != nil, "magic link token")
	s.NeedsPublic(t, requestMagicLink, redeemMagicLink)

	if s.Seams().RegistrationClosed {
		conformance.Skip(t, "conformance: this subject closes its sign-up door (Seams.RegistrationClosed), so nobody can be registered without a password")
	}

	registrar := s.Subject(t, conformance.Making(register), conformance.InTenant(signInSurface, tenancy.Global()))
	if registrar.Surfaces.SignIn == nil {
		conformance.Skip(t, "conformance: this subject mounts no sign-in surface, so nobody can be registered without a password")
	}

	username := "conf_" + identifiers.New()
	email := identifiers.New() + "@conformance.invalid"

	_, err := registrar.Surfaces.SignIn.Register(registrar.Context(t.Context()), &signinpb.RegisterRequest{
		User: &identitypb.UserRegistrationInput{
			Username:     username,
			EmailAddress: email,
			FirstName:    "Some",
		},
		Account:    &identitypb.AccountCreationInput{Name: username + "'s"},
		Agreements: everyAgreement(),
		Credential: &signinpb.RegisterRequest_NoPassword{NoPassword: &signinpb.NoPassword{}},
	})
	must.NoError(t, err, must.Sprint("registering somebody with no password"))

	anon := s.Subject(t, conformance.Making(requestMagicLink, redeemMagicLink))

	_, err = anon.Surfaces.SignIn.RequestMagicLink(t.Context(), &signinpb.RequestMagicLinkRequest{EmailAddress: email})
	must.NoError(t, err, must.Sprint("requesting a sign-in link"))

	secret, err := read(t.Context(), tenancy.Global(), email)
	must.NoError(t, err, must.Sprint("reading the sign-in link the deployment mailed"))

	redeemed, err := anon.Surfaces.SignIn.RedeemMagicLink(t.Context(), &signinpb.RedeemMagicLinkRequest{Token: secret})
	must.NoError(t, err, must.Sprint("the sign-in link the deployment mailed signed nobody in"))

	return s.SignedIn(t, redeemed.GetToken(), conformance.Making(selfService...), conformance.InTenant(surface, tenancy.Global()))
}

// everyAgreement is every document a registrant can accept, read off the enum,
// for the reason conformance/signin gives: whether any are required is the
// deployment's policy, and naming every one satisfies any such policy.
func everyAgreement() []identitypb.Agreement {
	var agreements []identitypb.Agreement

	for number := range identitypb.Agreement_name {
		if agreement := identitypb.Agreement(number); agreement != identitypb.Agreement_AGREEMENT_UNSPECIFIED {
			agreements = append(agreements, agreement)
		}
	}

	slices.Sort(agreements)

	return agreements
}
