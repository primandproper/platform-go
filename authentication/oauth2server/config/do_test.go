package oauth2servercfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newInjector registers the prerequisites both registrations share.
func newInjector(t *testing.T, cfg *Config) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue(i, cfg)

	return i
}

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	T.Run("resolves without any observability registered", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, &Config{Provider: ProviderMemory})
		RegisterStore(i)

		// A container that registers no pillars still wires up: absent is fine,
		// and is what InvokePillars distinguishes from "the registered one
		// failed to build".
		store, err := do.Invoke[oauth2server.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("uses the registered pillars when there are some", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, &Config{Provider: ProviderMemory})
		do.ProvideValue[logging.Logger](i, loggingnoop.NewLogger())
		RegisterStore(i)

		store, err := do.Invoke[oauth2server.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("a registered client that cannot build is an error rather than a fallback", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, &Config{Provider: ProviderMemory})
		do.Provide(i, func(do.Injector) (database.Client, error) {
			return nil, errBrokenClient
		})
		RegisterStore(i)

		// "Nobody registered one" and "the registered one failed to build" are
		// different things, and only the first is fine. Degrading to a store
		// that looks configured is how a misconfigured deployment comes up.
		store, err := do.Invoke[oauth2server.Store](i)
		test.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("an unknown provider fails the container", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, &Config{Provider: "redis"})
		RegisterStore(i)

		_, err := do.Invoke[oauth2server.Store](i)
		test.Error(t, err)
	})

	T.Run("a registered pillar that cannot build is an error rather than a noop", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, &Config{Provider: ProviderMemory})
		do.Provide(i, func(do.Injector) (logging.Logger, error) {
			return nil, errBrokenLogger
		})
		RegisterStore(i)

		// The same distinction the database client gets: nobody registering a
		// logger is fine, and a registered one whose exporter is misconfigured
		// is not — a store that came up unobserved would look configured.
		store, err := do.Invoke[oauth2server.Store](i)
		test.Error(t, err)
		test.Nil(t, store)
	})
}

func TestRegisterServer(T *testing.T) {
	T.Parallel()

	T.Run("resolves with an authenticator registered", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, &Config{Provider: ProviderMemory, Issuer: "https://auth.example"})
		do.ProvideValue[oauth2server.SubjectAuthenticator](i, testAuthenticator)
		RegisterServer(i)

		srv, err := do.Invoke[*oauth2server.Server](i)
		must.NoError(t, err)
		must.NotNil(t, srv)
		test.EqOp(t, "https://auth.example", srv.Issuer())
	})

	T.Run("a registered pillar that cannot build fails the server too", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, &Config{Provider: ProviderMemory, Issuer: "https://auth.example"})
		do.ProvideValue[oauth2server.SubjectAuthenticator](i, testAuthenticator)
		do.Provide(i, func(do.Injector) (logging.Logger, error) {
			return nil, errBrokenLogger
		})
		RegisterServer(i)

		srv, err := do.Invoke[*oauth2server.Server](i)
		test.Error(t, err)
		test.Nil(t, srv)
	})

	T.Run("a registered client that cannot build fails the server too", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, &Config{Provider: ProviderMemory, Issuer: "https://auth.example"})
		do.ProvideValue[oauth2server.SubjectAuthenticator](i, testAuthenticator)
		do.Provide(i, func(do.Injector) (database.Client, error) {
			return nil, errBrokenClient
		})
		RegisterServer(i)

		// Optional under the memory provider and still not something to
		// swallow: the container was told to build a client and could not.
		srv, err := do.Invoke[*oauth2server.Server](i)
		test.Error(t, err)
		test.Nil(t, srv)
	})

	T.Run("a container with no authenticator does not come up", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, &Config{Provider: ProviderMemory, Issuer: "https://auth.example"})
		RegisterServer(i)

		// A hard prerequisite rather than an optional one, unlike the database
		// client: there is no such thing as an authorization server that cannot
		// tell who the human is, so a container missing one fails rather than
		// coming up with something that cannot authenticate anybody.
		srv, err := do.Invoke[*oauth2server.Server](i)
		test.Error(t, err)
		test.Nil(t, srv)
	})
}

// errBrokenClient stands in for a database client whose own construction
// failed.
var errBrokenClient = platformerrors.New("the database client could not be built")

// errBrokenLogger stands in for a pillar whose own construction failed — a
// misconfigured exporter, most often.
var errBrokenLogger = platformerrors.New("the logger could not be built")
