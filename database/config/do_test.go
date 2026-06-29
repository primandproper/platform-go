package databasecfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v2/database"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

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
				Database: ":memory:",
			},
			WriteConnection: ConnectionDetails{
				Database: ":memory:",
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
}

func TestProvideClientConfig(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Provider: ProviderPostgres,
		}
		cc := ProvideClientConfig(cfg)
		must.NotNil(t, cc)
	})
}
