package jobscfg

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v13/database"
	databasecfg "github.com/primandproper/platform-go/v13/database/config"
	distributedlockcfg "github.com/primandproper/platform-go/v13/distributedlock/config"
	"github.com/primandproper/platform-go/v13/jobs"
	messagequeuecfg "github.com/primandproper/platform-go/v13/messagequeue/config"

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

func TestRegisterPool(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &PoolConfig{
			Queue: messagequeuecfg.Config{
				Consumer: messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderNoop},
			},
			Pool: jobs.PoolConfig{Topic: "example"},
		})
		do.ProvideValue[jobs.Handler](i, func(context.Context, []byte) error { return nil })

		RegisterPool(i)

		pool, err := do.Invoke[*jobs.Pool](i)
		must.NoError(t, err)
		test.NotNil(t, pool)
	})

	T.Run("errors when no jobs.Handler is registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &PoolConfig{
			Queue: messagequeuecfg.Config{
				Consumer: messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderNoop},
			},
			Pool: jobs.PoolConfig{Topic: "example"},
		})

		RegisterPool(i)

		pool, err := do.Invoke[*jobs.Pool](i)
		must.Error(t, err)
		test.Nil(t, pool)
	})
}

func TestRegisterScheduler(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &SchedulerConfig{
			Lock: distributedlockcfg.Config{Provider: distributedlockcfg.MemoryProvider},
		})

		RegisterScheduler(i)

		scheduler, err := do.Invoke[*jobs.Scheduler](i)
		must.NoError(t, err)
		test.NotNil(t, scheduler)
	})
}
