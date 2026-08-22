package sagacfg

import (
	"context"
	"path/filepath"
	"testing"

	cachecfg "github.com/primandproper/platform-go/v13/cache/config"
	"github.com/primandproper/platform-go/v13/database"
	databasecfg "github.com/primandproper/platform-go/v13/database/config"
	"github.com/primandproper/platform-go/v13/distributedlock"
	distributedlockcfg "github.com/primandproper/platform-go/v13/distributedlock/config"
	"github.com/primandproper/platform-go/v13/idempotency"
	idempotencycfg "github.com/primandproper/platform-go/v13/idempotency/config"
	outboxcfg "github.com/primandproper/platform-go/v13/outbox/config"
	"github.com/primandproper/platform-go/v13/saga"

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

		store, err := do.Invoke[saga.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})
}

func TestRegisterOutboxEventPublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, &outboxcfg.Config{})

		outboxcfg.RegisterWriter(i)
		RegisterOutboxEventPublisher(i)

		publisher, err := do.Invoke[saga.EventPublisher](i)
		must.NoError(t, err)
		test.NotNil(t, publisher)
	})

	T.Run("errors when no *outbox.Writer is registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{})

		RegisterOutboxEventPublisher(i)

		publisher, err := do.Invoke[saga.EventPublisher](i)
		must.Error(t, err)
		test.Nil(t, publisher)
	})
}

func TestRegisterWorker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		client := testDBClient(t)
		do.ProvideValue[database.Client](i, client)
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, &outboxcfg.Config{})
		do.ProvideValue(i, saga.NewRegistry())

		locker, err := distributedlockcfg.NewScopedLocker(
			t.Context(),
			&distributedlockcfg.Config{Provider: distributedlockcfg.MemoryProvider},
			nil,
		)
		must.NoError(t, err)
		do.ProvideValue[distributedlock.ScopedLocker](i, locker)

		manager, err := idempotencycfg.NewManager[saga.StepResult](t.Context(), &idempotencycfg.Config{
			Lock:  distributedlockcfg.Config{Provider: distributedlockcfg.MemoryProvider},
			Cache: cachecfg.Config{Provider: cachecfg.ProviderMemory},
		}, client)
		must.NoError(t, err)
		do.ProvideValue[*idempotency.Manager[saga.StepResult]](i, manager)

		outboxcfg.RegisterWriter(i)
		RegisterOutboxEventPublisher(i)
		RegisterStore(i)
		RegisterWorker(i)

		worker, err := do.Invoke[*saga.Worker](i)
		must.NoError(t, err)
		test.NotNil(t, worker)
	})
}
