package oauth2clients

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func registrations(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a registration minted here belongs to nobody, not to the caller", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		issued := register(t, caller)

		test.EqOp(t, "", issued.GetClient().GetBelongsToUser(),
			test.Sprint("the administered create wrote the caller as the owner"))

		read, err := caller.Surfaces.OAuth2Clients.GetOAuth2Client(caller.Context(t.Context()),
			&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: issued.GetClient().GetId()})
		must.NoError(t, err)
		test.EqOp(t, issued.GetClient().GetId(), read.GetResult().GetId())
		test.EqOp(t, "", read.GetResult().GetBelongsToUser(),
			test.Sprint("the stored registration names an owner the create did not answer with"))
	})

	t.Run("the secret is on the wire once, at creation, and never on a read", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		issued := register(t, caller)
		secret := issued.GetClientSecret()

		// A registration whose secret never reached its creator is one nobody
		// can use.
		must.StrNotEqFold(t, "", secret, must.Sprint("the create answered with no secret"))

		read, err := caller.Surfaces.OAuth2Clients.GetOAuth2Client(caller.Context(t.Context()),
			&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: issued.GetClient().GetId()})
		must.NoError(t, err)

		// The whole rendered message rather than a named field, so a secret
		// that arrived in a field added later is caught too.
		test.StrNotContains(t, read.String(), secret, test.Sprint("a read rendered a registration's secret"))

		page, err := caller.Surfaces.OAuth2Clients.ListOAuth2Clients(caller.Context(t.Context()),
			&oauth2clientspb.ListOAuth2ClientsRequest{})
		must.NoError(t, err)
		test.StrNotContains(t, page.String(), secret, test.Sprint("a listing rendered a registration's secret"))
	})

	// A nil input message is a malformed request rather than a registration
	// with no name, and the difference is what the caller is told: refused
	// here it names the input, and let through it would come back as a message
	// about a field the request never carried.
	t.Run("a create with no input is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := caller.Surfaces.OAuth2Clients.CreateOAuth2Client(caller.Context(t.Context()),
			&oauth2clientspb.CreateOAuth2ClientRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}
