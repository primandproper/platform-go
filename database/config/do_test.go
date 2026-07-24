package databasecfg

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v6/database"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterClientConfig(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, &Config{
			Provider: ProviderSQLite,
			ReadConnection: ConnectionDetails{
				Database: ":memory:",
			},
		})

		RegisterClientConfig(i)

		cc, err := do.Invoke[database.ClientConfig](i)
		must.NoError(t, err)
		test.NotNil(t, cc)
	})
}

func TestRegisterDatabase(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, nil)
		do.ProvideValue[database.Migrator](i, nil)
		do.ProvideValue(i, &Config{
			Provider: ProviderSQLite,
			ReadConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
			WriteConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
		})

		RegisterDatabase(i)

		client, err := do.Invoke[database.Client](i)
		must.NoError(t, err)
		test.NotNil(t, client)

		cc, err := do.Invoke[database.ClientConfig](i)
		must.NoError(t, err)
		test.NotNil(t, cc)
	})

	T.Run("errors when RunMigrations is true but no Migrator is registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, nil)
		// Deliberately do NOT register a database.Migrator, even though migrations are enabled.
		do.ProvideValue(i, &Config{
			Provider:      ProviderSQLite,
			RunMigrations: true,
			ReadConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
			WriteConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
		})

		RegisterDatabase(i)

		client, err := do.Invoke[database.Client](i)
		must.Error(t, err)
		test.Nil(t, client)
	})

	T.Run("builds without a registered Migrator when RunMigrations is false", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, nil)
		// Deliberately do NOT register a database.Migrator.
		do.ProvideValue(i, &Config{
			Provider:      ProviderSQLite,
			RunMigrations: false,
			ReadConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
			WriteConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
		})

		RegisterDatabase(i)

		client, err := do.Invoke[database.Client](i)
		must.NoError(t, err)
		test.NotNil(t, client)
	})
}

func TestNewClientConfig(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Provider: ProviderPostgres,
		}
		cc := NewClientConfig(cfg)
		must.NotNil(t, cc)
	})
}
