package signin

import (
	"testing"

	domain "github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// requestLink asks for a sign-in link for an address, as the form nobody has
// signed in to does.
func requestLink(t *testing.T, anon signinpb.SignInServiceClient, emailAddress string) *signinpb.RequestMagicLinkResponse {
	t.Helper()

	response, err := anon.RequestMagicLink(t.Context(), &signinpb.RequestMagicLinkRequest{EmailAddress: emailAddress})
	must.NoError(t, err, must.Sprint("requesting a sign-in link"))

	return response
}

// mailedLink is the sign-in link the deployment most recently mailed to an
// address, or a skip where the subject cannot say.
func mailedLink(t *testing.T, s *conformance.Session, emailAddress string) string {
	t.Helper()

	read := s.Seams().Actions.MagicLinkToken
	s.NeedsAction(t, read != nil, "magic link token")

	secret, err := read(t.Context(), tenancy.Global(), emailAddress)
	must.NoError(t, err, must.Sprint("reading the sign-in link the deployment mailed"))
	must.NotEqOp(t, "", secret, must.Sprint("the deployment mailed an empty sign-in link"))

	return secret
}

// redeem answers a sign-in link.
func redeem(t *testing.T, anon signinpb.SignInServiceClient, token string) (*signinpb.IssuedToken, error) {
	t.Helper()

	response, err := anon.RedeemMagicLink(t.Context(), &signinpb.RedeemMagicLinkRequest{Token: token})
	if err != nil {
		return nil, err
	}

	return response.GetToken(), nil
}

func magicLinks(t *testing.T, s *conformance.Session) {
	t.Helper()

	// The flow the passwordless door was added for: somebody arrives naming no
	// password and gets in on one mail, from a client with nobody on it.
	t.Run("somebody with no password signs in on one mailed link", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)
		who, _ := register(t, s, withNoPassword(registrationRequest()))

		requestLink(t, anon, who.email)

		issued, err := redeem(t, anon, mailedLink(t, s, who.email))
		must.NoError(t, err, must.Sprint("the link the deployment mailed did not sign anybody in"))

		test.NotEqOp(t, "", issued.GetToken())
		test.NotEqOp(t, "", issued.GetFamilyId())
		test.EqOp(t, who.accountID, issued.GetActiveAccountId())
	})

	// The half that makes one mail enough: a registrant who has not proven
	// their address cannot sign in with a password, and a sign-in link proves
	// it. The password door refusing and then admitting them is what says the
	// link moved their standing, rather than only minting a token past it.
	t.Run("a sign-in link finishes a registration", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)
		who, _ := register(t, s, withPassword(registrationRequest()))

		_, err := login(t.Context(), anon, who.username, password, "")
		refused(t, err, codes.FailedPrecondition, reasonUserUnverified)

		requestLink(t, anon, who.email)

		_, err = redeem(t, anon, mailedLink(t, s, who.email))
		must.NoError(t, err)

		loggedIn(t, anon, who.username, password)
	})

	// The enumeration defense at the transport, which is where a consumer is
	// likeliest to undo it: an address nobody holds and one somebody does get
	// the same answer. The control is that the known address really was mailed,
	// since two identical answers from a deployment that mails nobody would
	// prove nothing.
	t.Run("a request for an unknown address is answered as one for a known address", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)
		who, _ := register(t, s, withNoPassword(registrationRequest()))

		known := requestLink(t, anon, who.email)
		mailedLink(t, s, who.email)

		unknown := requestLink(t, anon, freshEmail())

		test.True(t, proto.Equal(known, unknown),
			test.Sprint("a sign-in link request told a known address apart from an unknown one"))
	})

	// A link is spent by its first use, and every way a link fails — never
	// mailed, already spent — is one answer, the one a wrong password gets.
	t.Run("a dead sign-in link is refused as a wrong password is", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s)
		who, _ := register(t, s, withNoPassword(registrationRequest()))

		_, never := redeem(t, anon, identifiers.New())
		refused(t, never, codes.Unauthenticated, reasonInvalidCredentials)
		test.EqOp(t, domain.ErrInvalidCredentials.Error(), status.Convert(never).Message())

		requestLink(t, anon, who.email)
		link := mailedLink(t, s, who.email)

		_, err := redeem(t, anon, link)
		must.NoError(t, err, must.Sprint("the control: the link the deployment mailed did not sign in"))

		_, spent := redeem(t, anon, link)
		indistinguishable(t, never, spent, "a spent sign-in link against one never mailed")
	})
}
