package webauthncfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterSessionStore(T *testing.T) {
	T.Parallel()

	T.Run("uses a registered database client under the database provider", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, newTestClient(t))
		do.ProvideValue(i, databaseConfig())

		RegisterSessionStore(i)

		store, err := do.Invoke[webauthn.SessionStore](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	// A container running the cache provider should not have to register a
	// database client it will never use.
	T.Run("resolves with no database client registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, cacheConfig())

		RegisterSessionStore(i)

		store, err := do.Invoke[webauthn.SessionStore](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	// A registered provider that fails to build has to reach the caller.
	// Treating it as absent would leave a misconfigured exporter looking
	// configured — see observability.InvokePillars.
	T.Run("a failing observability provider is an error", func(t *testing.T) {
		t.Parallel()

		errBuild := errors.New("building the metrics provider")

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, cacheConfig())
		do.Provide(i, func(do.Injector) (metrics.Provider, error) {
			return nil, errBuild
		})

		RegisterSessionStore(i)

		_, err := do.Invoke[webauthn.SessionStore](i)
		must.Error(t, err)
		test.ErrorIs(t, err, errBuild)
	})

	// The other half of the same distinction: a database client that was
	// registered and could not be built must not be reported as one that was
	// never registered.
	T.Run("a failing database client is an error", func(t *testing.T) {
		t.Parallel()

		errBuild := errors.New("building the database client")

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, cacheConfig())
		do.Provide(i, func(do.Injector) (database.Client, error) {
			return nil, errBuild
		})

		RegisterSessionStore(i)

		_, err := do.Invoke[webauthn.SessionStore](i)
		must.Error(t, err)
		test.ErrorIs(t, err, errBuild)
	})
}

func TestRegisterRelyingParty(T *testing.T) {
	T.Parallel()

	T.Run("resolves a relying party and the store behind it", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, newTestClient(t))
		do.ProvideValue(i, databaseConfig())

		RegisterRelyingParty(i)

		rp, err := do.Invoke[*webauthn.RelyingParty](i)
		must.NoError(t, err)
		must.NotNil(t, rp)

		creation, err := rp.BeginRegistration(t.Context(), &testUser{})
		must.NoError(t, err)
		test.NotNil(t, creation)
	})

	T.Run("reports a config it cannot build a store from", func(t *testing.T) {
		t.Parallel()

		// No database client, under the provider that requires one.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, databaseConfig())

		RegisterRelyingParty(i)

		_, err := do.Invoke[*webauthn.RelyingParty](i)
		must.Error(t, err)
	})
}
