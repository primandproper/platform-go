package oauth2clients

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test/must"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "oauth2clients"

// Suite is the OAuth2 client registry surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
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

// twoRegistries mints an operator in each of two tenants, and refuses to
// proceed if the subject put them in one, which would make every confinement
// assertion here compare a registry with itself.
//
// Operators, because every call on this surface is one: the registry is the
// deployment's list of who may ask it for tokens.
func twoRegistries(t *testing.T, s *conformance.Session, methods ...string) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.TwoTenants(t, surface)

	return s.OperatorIn(t, surface, mine.ScopeFor(surface), methods...), s.OperatorIn(t, surface, theirs.ScopeFor(surface), methods...)
}

// colleague mints a second operator in of's registry. A subject that cannot
// put two callers in one tenant declines, and one that answers the same
// administrator for every request in a tenant cannot show a registry being the
// tenant's rather than the registrar's; the assertion that asked skips either
// way.
func colleague(t *testing.T, s *conformance.Session, of *conformance.Subject, methods ...string) *conformance.Subject {
	t.Helper()

	other := s.OperatorIn(t, surface, of.ScopeFor(surface), methods...)

	if of.UserID == other.UserID {
		t.Skip("conformance: the subject answers one administrator for every request in a tenant, so a colleague's registration cannot be told from the caller's own")
	}

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
