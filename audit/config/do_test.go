package auditcfg

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v10/audit"
	"github.com/primandproper/platform-go/v10/database"
	databasecfg "github.com/primandproper/platform-go/v10/database/config"
	"github.com/primandproper/platform-go/v10/database/dialect"

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

func testConfig() *Config {
	return &Config{Dialect: dialect.SQLite}
}

func TestRegisterRecorder(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, testConfig())

		RegisterRecorder(i)

		recorder, err := do.Invoke[audit.Recorder](i)
		must.NoError(t, err)
		test.NotNil(t, recorder)
	})
}

func TestRegisterReader(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, testConfig())

		RegisterReader(i)

		reader, err := do.Invoke[audit.Reader](i)
		must.NoError(t, err)
		test.NotNil(t, reader)
	})
}
