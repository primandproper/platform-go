package oauth2clientscfg

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"

	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func testDBClient(t *testing.T) database.Client {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	client, err := databasecfg.NewDatabase(t.Context(), &databasecfg.Config{
		Provider:        databasecfg.ProviderSQLite,
		ReadConnection:  databasecfg.ConnectionDetails{Database: path},
		WriteConnection: databasecfg.ConnectionDetails{Database: path},
	}, nil)
	must.NoError(t, err)

	return client
}

// base registers what both registrations below need, and neither of them, so
// each case says only what it is about.
func base(t *testing.T, cfg *Config) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue[database.Client](i, testDBClient(t))
	do.ProvideValue(i, cfg)

	return i
}

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{})
		RegisterStore(i)

		store, err := do.Invoke[oauth2clients.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("carries the configured prefix", func(t *testing.T) {
		t.Parallel()

		// The prefix is the whole of what this package configures, so it is the
		// one thing worth reading back off what the container built.
		i := base(t, &Config{TablePrefix: "ddb"})
		RegisterStore(i)

		store, err := do.Invoke[oauth2clients.Store](i)
		must.NoError(t, err)

		sqlStore, ok := store.(*oauth2clients.SQLStore)
		must.True(t, ok)
		test.EqOp(t, "ddb", sqlStore.TablePrefix())
	})

	T.Run("surfaces a bad config", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{TablePrefix: "has space"})
		RegisterStore(i)

		_, err := do.Invoke[oauth2clients.Store](i)
		must.Error(t, err)
	})
}

func TestRegisterService(T *testing.T) {
	T.Parallel()

	T.Run("resolves without hooks registered", func(t *testing.T) {
		t.Parallel()

		// A container that registers no Hooks is an application with nothing
		// to commit beside a registration, which is a configuration rather than
		// a hole.
		i := base(t, &Config{})
		RegisterStore(i)
		RegisterService(i)

		_, err := do.Invoke[oauth2clients.Hooks](i)
		test.Error(t, err, test.Sprint("this case is only meaningful with no Hooks registered"))

		svc, err := do.Invoke[*oauth2clients.Service](i)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("uses the hooks the container holds", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{})
		do.ProvideValue[oauth2clients.Hooks](i, oauth2clients.NoopHooks{})
		RegisterStore(i)
		RegisterService(i)

		svc, err := do.Invoke[*oauth2clients.Service](i)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("a hooks provider that fails to build fails the service", func(t *testing.T) {
		t.Parallel()

		// Absent is a configuration; registered-and-broken is not, and a Service
		// that ran the noop in its place would commit every registration with
		// none of the companions the consumer registered hooks to get.
		boom := errors.New("audit sink unreachable")

		i := base(t, &Config{})
		do.Provide(i, func(do.Injector) (oauth2clients.Hooks, error) { return nil, boom })
		RegisterStore(i)
		RegisterService(i)

		svc, err := do.Invoke[*oauth2clients.Service](i)
		test.Nil(t, svc)
		test.ErrorIs(t, err, boom)
	})

	T.Run("a hooks provider that needs an unregistered dependency fails the service", func(t *testing.T) {
		t.Parallel()

		// The same failure by the route that reports do's own not-found
		// sentinel, which a lookup keyed on that sentinel would have read as
		// "nobody registered hooks".
		i := base(t, &Config{})
		do.Provide(i, func(i do.Injector) (oauth2clients.Hooks, error) {
			if _, err := do.Invoke[*unregisteredRecorder](i); err != nil {
				return nil, err
			}

			return oauth2clients.NoopHooks{}, nil
		})
		RegisterStore(i)
		RegisterService(i)

		svc, err := do.Invoke[*oauth2clients.Service](i)
		test.Nil(t, svc)
		test.ErrorIs(t, err, do.ErrServiceNotFound)
	})

	T.Run("needs a store", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{})
		RegisterService(i)

		svc, err := do.Invoke[*oauth2clients.Service](i)
		test.Nil(t, svc)
		test.ErrorIs(t, err, do.ErrServiceNotFound)
	})
}

// unregisteredRecorder stands in for a dependency a consumer's hooks provider
// asks the container for and nothing registered.
type unregisteredRecorder struct{}

// TestAPillarsProviderThatFailsToBuildFailsEveryRegistration is the line
// observability.InvokePillars draws: absent observability wires up silently,
// and a registered provider that cannot be built is a failure rather than a
// component that looks configured and exports nothing.
func TestAPillarsProviderThatFailsToBuildFailsEveryRegistration(T *testing.T) {
	T.Parallel()

	boom := errors.New("the collector is unreachable")

	build := func(t *testing.T) do.Injector {
		t.Helper()

		i := base(t, &Config{})
		do.Provide(i, func(do.Injector) (*observability.Pillars, error) { return nil, boom })
		RegisterStore(i)
		RegisterService(i)

		return i
	}

	T.Run("store", func(t *testing.T) {
		t.Parallel()

		store, err := do.Invoke[oauth2clients.Store](build(t))
		test.Nil(t, store)
		test.ErrorIs(t, err, boom)
	})

	T.Run("service", func(t *testing.T) {
		t.Parallel()

		svc, err := do.Invoke[*oauth2clients.Service](build(t))
		test.Nil(t, svc)
		test.ErrorIs(t, err, boom)
	})
}
