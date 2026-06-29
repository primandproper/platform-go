package databasecfg

import (
	"context"

	"github.com/primandproper/platform-go/v2/database"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterClientConfig registers a database.ClientConfig with the injector.
func RegisterClientConfig(i do.Injector) {
	do.Provide[database.ClientConfig](i, func(i do.Injector) (database.ClientConfig, error) {
		cfg := do.MustInvoke[*Config](i)
		return ProvideClientConfig(*cfg), nil
	})
}

// RegisterDatabase registers a database.Client with the injector.
// Prerequisite: *Config and database.Migrator must be registered in the injector.
func RegisterDatabase(i do.Injector) {
	RegisterClientConfig(i)
	do.Provide[database.Client](i, func(i do.Injector) (database.Client, error) {
		return ProvideDatabase(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Migrator](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})
}
