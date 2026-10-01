package oauth2server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"golang.org/x/oauth2"
)

// discovery is the wiring check: the document a client starts from names the
// endpoints where this router serves them.
//
// The issuer is not compared with BaseURL. A deployment behind a proxy
// publishes its public origin, and that is the right answer for it; what has to
// hold is that the paths the document names are the ones mounted.
func discovery(t *testing.T, srv *server) {
	t.Helper()

	t.Run("the discovery document names the endpoints where they are mounted", func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			srv.base+oauth2server.PathAuthorizationServerMetadata, http.NoBody)
		must.NoError(t, err)

		answered := srv.do(t, req)
		must.EqOp(t, http.StatusOK, answered.status,
			must.Sprintf("the discovery document answered %d: %s", answered.status, answered.body))

		var metadata oauth2server.AuthorizationServerMetadata
		must.NoError(t, json.Unmarshal(answered.body, &metadata), must.Sprintf("reading the discovery document: %s", answered.body))

		test.StrHasSuffix(t, oauth2server.PathAuthorize, metadata.AuthorizationEndpoint)
		test.StrHasSuffix(t, oauth2server.PathToken, metadata.TokenEndpoint)
		test.StrHasSuffix(t, oauth2server.PathRevoke, metadata.RevocationEndpoint)
	})
}

// clients is what the registry decorator answers for a client at /token, where
// a code is never reached: a registration it issued authenticates with its
// secret, and with nothing else.
func clients(t *testing.T, s *conformance.Session, srv *server) {
	t.Helper()

	t.Run("a client authenticates with its own secret and is refused with any other", func(t *testing.T) {
		t.Parallel()

		f := newFlow(t, s, srv)
		// A well-formed verifier, so the request is refused on the code rather
		// than on a verifier PKCE would not accept.
		code := authorization{code: "conformance-never-issued", verifier: oauth2.GenerateVerifier()}

		// The control: with its own secret the client is past authentication,
		// and refused on the code it named — so the refusal below is about the
		// secret rather than a client the server cannot find.
		f.exchange(t, code).refused(t, http.StatusBadRequest, errorInvalidGrant, "exchanging a code nobody issued")

		f.client.secret += "-wrong"
		f.exchange(t, code).refused(t, http.StatusUnauthorized, errorInvalidClient, "exchanging with the wrong client secret")
	})
}
