package outboxcfg

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v14/outbox"

	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	messagequeuecfg "github.com/primandproper/primitives-go/v2/messagequeue/config"

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

func TestRegisterWriter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})

		RegisterWriter(i)

		writer, err := do.Invoke[*outbox.Writer](i)
		must.NoError(t, err)
		test.NotNil(t, writer)
	})
}

func TestRegisterRelay(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{
			Queue: messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderNoop},
		})

		RegisterRelay(i)

		relay, err := do.Invoke[*outbox.Relay](i)
		must.NoError(t, err)
		test.NotNil(t, relay)
	})
}
