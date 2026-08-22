package sessionscfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/cookies"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/sessions"
	sessionshttp "github.com/primandproper/platform-go/v13/sessions/http"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	// The cache provider is the default, and a container running it should not
	// have to register a database client it will never use.
	T.Run("resolves with no database client registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, memoryConfig())

		RegisterStore[principal](i)

		store, err := do.Invoke[sessions.Store[principal]](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("uses a registered database client under the database provider", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, newTestClient(t, ""))
		do.ProvideValue(i, &Config{Provider: ProviderDatabase})

		RegisterStore[principal](i)

		store, err := do.Invoke[sessions.Store[principal]](i)
		must.NoError(t, err)

		session, err := store.New(t.Context(), &principal{UserID: "u_1"})
		must.NoError(t, err)

		_, err = store.Get(t.Context(), session.ID)
		must.NoError(t, err)
	})

	// A registered provider that fails to build has to reach the caller.
	// Treating it as absent would leave a misconfigured exporter looking
	// configured — see observability.InvokePillars.
	T.Run("a failing observability provider is an error", func(t *testing.T) {
		t.Parallel()

		errBuild := errors.New("building the metrics provider")

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, memoryConfig())
		do.Provide(i, func(do.Injector) (metrics.Provider, error) {
			return nil, errBuild
		})

		RegisterStore[principal](i)

		_, err := do.Invoke[sessions.Store[principal]](i)
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
		do.ProvideValue(i, memoryConfig())
		do.Provide(i, func(do.Injector) (database.Client, error) {
			return nil, errBuild
		})

		RegisterStore[principal](i)

		_, err := do.Invoke[sessions.Store[principal]](i)
		must.Error(t, err)
		test.ErrorIs(t, err, errBuild)
	})
}

func TestRegisterManager(T *testing.T) {
	T.Parallel()

	T.Run("resolves a manager and the store behind it", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, memoryConfig())
		do.ProvideValue[cookies.Manager](i, newTestCookieManager(t))

		RegisterManager[principal](i)

		manager, err := do.Invoke[*sessionshttp.Manager[principal]](i)
		must.NoError(t, err)
		test.NotNil(t, manager)
	})
}
