package oauth2clients

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test/must"
)

// Suite is the OAuth2 client registry surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    "oauth2clients",
		Mounted: func(s conformance.Surfaces) bool { return s.OAuth2Clients != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("registrations", func(t *testing.T) {
		t.Parallel()
		registrations(t, s)
	})
	t.Run("reach", func(t *testing.T) {
		t.Parallel()
		reach(t, s)
	})
}

// redirect is a redirect URI any validator accepts, so that an assertion about
// the registry is not also an assertion about URI validation.
const redirect = "https://example.test/callback"

// twoRegistries mints two callers and refuses to proceed if the subject put
// them in one tenant, which would make every confinement assertion here
// compare a registry with itself.
func twoRegistries(t *testing.T, s *conformance.Session) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.Subject(t), s.Subject(t)

	must.StrNotEqFold(t, mine.Scope.String(), theirs.Scope.String(),
		must.Sprint("the subject minted two callers in one tenant; the confinement this asserts cannot be observed"))

	return mine, theirs
}

// colleague mints a second caller in of's registry. A subject that cannot put
// two callers in one tenant declines, and the assertion that asked skips.
func colleague(t *testing.T, s *conformance.Session, of *conformance.Subject) *conformance.Subject {
	t.Helper()

	other := s.Subject(t, conformance.InTenant(of.Scope))

	must.StrNotEqFold(t, of.UserID, other.UserID,
		must.Sprint("the subject minted a colleague as the same user"))

	return other
}

// register mints an administered registration through the surface, and
// returns what the create answered with — the only message that carries the
// secret.
func register(t *testing.T, sub *conformance.Subject) *oauth2clientspb.IssuedOAuth2Client {
	t.Helper()

	created, err := sub.Surfaces.OAuth2Clients.CreateOAuth2Client(sub.Context(t.Context()),
		&oauth2clientspb.CreateOAuth2ClientRequest{Input: &oauth2clientspb.OAuth2ClientCreationInput{
			Name:         "conformance client",
			RedirectUris: []string{redirect},
		}})
	must.NoError(t, err, must.Sprint("minting a registration"))
	must.NotNil(t, created.GetIssued().GetClient())
	must.StrNotEqFold(t, "", created.GetIssued().GetClient().GetId())

	return created.GetIssued()
}

// registry is the identifiers on the first page of sub's registry.
func registry(t *testing.T, sub *conformance.Subject) []string {
	t.Helper()

	page, err := sub.Surfaces.OAuth2Clients.ListOAuth2Clients(sub.Context(t.Context()),
		&oauth2clientspb.ListOAuth2ClientsRequest{})
	must.NoError(t, err)
	must.NotNil(t, page.GetPagination(), must.Sprint("a paged read answered with no pagination"))

	ids := make([]string, 0, len(page.GetResults()))
	for _, c := range page.GetResults() {
		ids = append(ids, c.GetId())
	}

	return ids
}
