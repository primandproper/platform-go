package oauth2server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewResourceMetadata(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		meta, err := oauth2server.NewResourceMetadata(testResource, []string{testIssuer},
			oauth2server.WithResourceName("Recipes API"),
			oauth2server.WithResourceScopes("read", "write"),
			oauth2server.WithResourceDocumentation("https://docs.example/api"))
		must.NoError(t, err)

		doc := meta.Document()
		test.EqOp(t, testResource, doc.Resource)
		test.EqOp(t, "Recipes API", doc.ResourceName)
		test.Eq(t, []string{testIssuer}, doc.AuthorizationServers)
		test.Eq(t, []string{"read", "write"}, doc.ScopesSupported)

		// Header only. RFC 6750 also defines a query parameter, and advertising
		// it would be inviting bearer tokens into every access log and Referer
		// header between the client and here.
		test.Eq(t, []string{"header"}, doc.BearerMethodsSupported)
	})

	T.Run("rejects a document that would say nothing", func(t *testing.T) {
		t.Parallel()

		meta, err := oauth2server.NewResourceMetadata("", []string{testIssuer})
		test.ErrorIs(t, err, oauth2server.ErrEmptyResource)
		test.Nil(t, meta)

		// The document's entire purpose is to say where to go and get a token.
		meta, err = oauth2server.NewResourceMetadata(testResource, nil)
		test.ErrorIs(t, err, oauth2server.ErrNoAuthorizationServer)
		test.Nil(t, meta)
	})

	T.Run("what a read hands back cannot be written back through", func(t *testing.T) {
		t.Parallel()

		meta, err := oauth2server.NewResourceMetadata(testResource, []string{testIssuer})
		must.NoError(t, err)

		doc := meta.Document()
		doc.AuthorizationServers[0] = "https://attacker.example"

		test.Eq(t, []string{testIssuer}, meta.Document().AuthorizationServers)
	})

	T.Run("serves the document at the well-known path", func(t *testing.T) {
		t.Parallel()

		meta, err := oauth2server.NewResourceMetadata(testResource, []string{testIssuer})
		must.NoError(t, err)

		srv := httptest.NewServer(meta.Handler())
		t.Cleanup(srv.Close)

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			srv.URL+oauth2server.PathProtectedResourceMetadata, http.NoBody)
		must.NoError(t, err)

		res, err := srv.Client().Do(req)
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		must.EqOp(t, http.StatusOK, res.StatusCode)

		var doc oauth2server.ProtectedResourceMetadata
		must.NoError(t, json.NewDecoder(res.Body).Decode(&doc))
		test.EqOp(t, testResource, doc.Resource)
	})
}

// The challenge header is the half of discovery that is easy to leave out: a
// client with no token has no reason to fetch the metadata document until
// something points at it.
func TestResourceMetadata_Challenge(T *testing.T) {
	T.Parallel()

	meta, err := oauth2server.NewResourceMetadata(testResource, []string{testIssuer})
	must.NoError(T, err)

	T.Run("points at the metadata document", func(t *testing.T) {
		t.Parallel()

		challenge := meta.Challenge("", "")

		test.StrHasPrefix(t, "Bearer ", challenge)
		test.StrContains(t, challenge, oauth2server.PathProtectedResourceMetadata)

		// The resource identifier is conventionally written with a trailing
		// slash, and concatenating the path onto that renders a URL some
		// clients follow and some do not.
		test.StrNotContains(t, challenge, "//.well-known")

		// No error parameter: a request that carried no token is an absence
		// rather than a failure, and RFC 6750 §3.1 has no code for it.
		test.StrNotContains(t, challenge, "error=")
	})

	T.Run("names the error when there was one", func(t *testing.T) {
		t.Parallel()

		challenge := meta.Challenge("invalid_token", "the token expired")

		test.StrContains(t, challenge, `error="invalid_token"`)
		test.StrContains(t, challenge, `error_description="the token expired"`)
	})
}
