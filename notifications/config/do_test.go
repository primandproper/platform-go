package notificationscfg

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"

	"github.com/primandproper/primitives-go/database"
	databasecfg "github.com/primandproper/primitives-go/database/config"
	"github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability/metrics"

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

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})

		RegisterStore(i)

		store, err := do.Invoke[*notifications.SQLStore](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("the three registrations are one store", func(t *testing.T) {
		t.Parallel()

		// The interfaces are narrowings of the concrete registration rather than
		// registrations of their own, so a container invoking both seams gets
		// one connection and one set of instruments instead of two of each.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{TablePrefix: "ddb"})

		RegisterStore(i)

		store, err := do.Invoke[*notifications.SQLStore](i)
		must.NoError(t, err)

		inbox, err := do.Invoke[notifications.Inbox](i)
		must.NoError(t, err)

		registry, err := do.Invoke[notifications.Registry](i)
		must.NoError(t, err)

		test.True(t, inbox == notifications.Inbox(store))
		test.True(t, registry == notifications.Registry(store))
		test.EqOp(t, "ddb", store.TablePrefix())
	})

	T.Run("with no observability registered", func(t *testing.T) {
		t.Parallel()

		// A container that registers no pillars still wires up: absent is fine,
		// and only a registered one that fails to build is an error.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})

		RegisterStore(i)

		store, err := do.Invoke[notifications.Inbox](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("a failing observability provider reaches the caller", func(t *testing.T) {
		t.Parallel()

		// Asserted by identity, not merely that some error came back: a missing
		// config would also fail, and would not exercise this branch.
		errBuild := errors.New("building the metrics provider")

		i := do.New()
		do.Provide(i, func(do.Injector) (metrics.Provider, error) {
			return nil, errBuild
		})

		RegisterStore(i)

		_, err := do.Invoke[*notifications.SQLStore](i)
		must.Error(t, err)
		test.ErrorIs(t, err, errBuild)
	})

	T.Run("a store that will not build leaves the seams nil rather than panicking", func(t *testing.T) {
		t.Parallel()

		// The narrowings return only once the store's error is known to be nil.
		// Handing a nil *SQLStore straight back would make a non-nil interface
		// holding a nil pointer, and this assertion would pass while the value
		// panicked on first use.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{TablePrefix: "has space"})

		RegisterStore(i)

		inbox, err := do.Invoke[notifications.Inbox](i)
		must.Error(t, err)
		test.Nil(t, inbox)

		registry, err := do.Invoke[notifications.Registry](i)
		must.Error(t, err)
		test.Nil(t, registry)
	})

	T.Run("fails when the database client is not registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{})

		RegisterStore(i)

		_, err := do.Invoke[*notifications.SQLStore](i)
		test.Error(t, err)
	})
}
