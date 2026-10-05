package service

import (
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	oauth2serverstorecfg "github.com/primandproper/platform-go/v15/authentication/oauth2serverstore/config"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// oauth2ServerConfig is the authorization server's block over the memory
// store, which needs no database for a test that only mounts.
func oauth2ServerConfig(t *testing.T) *Config {
	t.Helper()

	cfg := &Config{
		Name: "example",
		OAuth2Server: &oauth2serverstorecfg.Config{
			Provider: oauth2serverstorecfg.ProviderMemory,
			Issuer:   "https://example.com",
		},
	}
	must.NoError(t, cfg.ValidateWithContext(t.Context()))

	return cfg
}

// TestRegisterTransports_oauth2Server is the authorization server as the
// automatic mount puts it on the router: from its config block and the
// application's authenticator, with no seam from Transports.
func TestRegisterTransports_oauth2Server(T *testing.T) {
	T.Parallel()

	T.Run("mounts from its block and the application's authenticator, and serves its discovery document", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, oauth2ServerConfig(t))
		router := newRouter()
		do.ProvideValue(i, router)
		do.ProvideValue[oauth2server.SubjectAuthenticator](i, refusingAuthenticator{})

		// No extractor, no tenant reader and no authorizers: the server
		// authenticates its own callers, so it asks Transports for nothing.
		RegisterTransports(i, &Transports{})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)
		test.Eq(t, []string{"oauth2 server HTTP"}, mounted.names)
		test.SliceEmpty(t, mounted.registrations)

		res := httptest.NewRecorder()
		router.Handler().ServeHTTP(res, httptest.NewRequestWithContext(t.Context(),
			nethttp.MethodGet, oauth2server.PathAuthorizationServerMetadata, nethttp.NoBody))
		must.EqOp(t, nethttp.StatusOK, res.Code, must.Sprintf("the discovery document answered %s", res.Body))

		var metadata oauth2server.AuthorizationServerMetadata
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &metadata))
		test.EqOp(t, "https://example.com"+oauth2server.PathToken, metadata.TokenEndpoint)
	})

	T.Run("a block with no authenticator beside it fails the startup rather than mounting nothing", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, oauth2ServerConfig(t))
		do.ProvideValue(i, newRouter())

		RegisterTransports(i, &Transports{})

		_, err := do.Invoke[*mountedTransports](i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[*oauth2server.Server]())
	})

	T.Run("no block mounts no server, and that is not an error", func(t *testing.T) {
		t.Parallel()

		i := newTransportInjector(t)
		do.ProvideValue(i, newRouter())
		do.ProvideValue[oauth2server.SubjectAuthenticator](i, refusingAuthenticator{})

		RegisterTransports(i, &Transports{})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)
		test.SliceEmpty(t, mounted.names)
	})

	// A consumer that mounted the server itself before this surface existed
	// keeps doing so: Skip leaves the router to it.
	T.Run("skipping it leaves the routes to the application", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, oauth2ServerConfig(t))
		router := newRouter()
		do.ProvideValue(i, router)
		do.ProvideValue[oauth2server.SubjectAuthenticator](i, refusingAuthenticator{})

		RegisterTransports(i, &Transports{Skip: []Surface{SurfaceOAuth2Server}})

		mounted, err := do.Invoke[*mountedTransports](i)
		must.NoError(t, err)
		test.SliceEmpty(t, mounted.names)

		srv, err := do.Invoke[*oauth2server.Server](i)
		must.NoError(t, err)

		srv.Mount(router)
		test.NoError(t, router.Err())
	})
}
