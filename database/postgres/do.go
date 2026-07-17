package postgres

import (
	"context"

	"github.com/primandproper/platform-go/v5/database"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterDatabaseClient registers a database.Client with the injector.
// Prerequisite: database.ClientConfig must be registered (e.g. via databasecfg.RegisterClientConfig).
func RegisterDatabaseClient(i do.Injector) {
	do.Provide[database.Client](i, func(i do.Injector) (database.Client, error) {
		return NewDatabaseClient(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[database.ClientConfig](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})
}
