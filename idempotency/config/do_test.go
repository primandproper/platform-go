package idempotencycfg

import (
	"context"
	"path/filepath"
	"testing"

	cachecfg "github.com/primandproper/platform-go/v13/cache/config"
	"github.com/primandproper/platform-go/v13/database"
	databasecfg "github.com/primandproper/platform-go/v13/database/config"
	distributedlockcfg "github.com/primandproper/platform-go/v13/distributedlock/config"
	"github.com/primandproper/platform-go/v13/idempotency"

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

func TestRegisterManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{
			Lock:  distributedlockcfg.Config{Provider: distributedlockcfg.MemoryProvider},
			Cache: cachecfg.Config{Provider: cachecfg.ProviderMemory},
		})

		RegisterManager[string](i)

		manager, err := do.Invoke[*idempotency.Manager[string]](i)
		must.NoError(t, err)
		test.NotNil(t, manager)
	})
}
