package webhookscfg

import (
	"context"
	"path/filepath"
	"testing"

	outboxcfg "github.com/primandproper/platform-go/v15/outbox/config"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/errors"

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

		store, err := do.Invoke[webhooks.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})
}

func TestRegisterDispatcher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, webhooks.Catalog{})

		RegisterStore(i)
		RegisterDispatcher(i)

		dispatcher, err := do.Invoke[webhooks.Dispatcher](i)
		must.NoError(t, err)
		test.NotNil(t, dispatcher)
	})

	T.Run("errors when no webhooks.Catalog is registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})

		RegisterStore(i)
		RegisterDispatcher(i)

		dispatcher, err := do.Invoke[webhooks.Dispatcher](i)
		must.Error(t, err)
		test.Nil(t, dispatcher)
	})
}

func TestRegisterEmitter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, webhooks.Catalog{})
		do.ProvideValue(i, &outboxcfg.Config{})
		outboxcfg.RegisterWriter(i)

		RegisterEmitter(i)

		emitter, err := do.Invoke[*webhooks.Emitter](i)
		must.NoError(t, err)
		test.NotNil(t, emitter)
	})

	T.Run("errors when no outbox writer is registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, webhooks.Catalog{})

		RegisterEmitter(i)

		emitter, err := do.Invoke[*webhooks.Emitter](i)
		must.Error(t, err)
		test.Nil(t, emitter)
	})

	T.Run("does not build over the registered store", func(t *testing.T) {
		t.Parallel()

		// The registered store's hooks may write through this emitter, so an
		// emitter that dispatched through it would be a store that needs itself.
		// One whose store provider fails proves the emitter never asked.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, webhooks.Catalog{})
		do.ProvideValue(i, &outboxcfg.Config{})
		outboxcfg.RegisterWriter(i)
		do.Provide(i, func(do.Injector) (webhooks.Store, error) {
			return nil, errors.New("the registered store was invoked")
		})

		RegisterEmitter(i)

		_, err := do.Invoke[*webhooks.Emitter](i)
		must.NoError(t, err)
	})
}

func TestRegisterWorker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})

		RegisterStore(i)
		RegisterWorker(i)

		worker, err := do.Invoke[*webhooks.Worker](i)
		must.NoError(t, err)
		test.NotNil(t, worker)
	})
}
