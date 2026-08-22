package sqlite

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterDatabaseClient registers a database.Client with the injector.
// Prerequisite: database.ClientConfig must be registered (e.g. via databasecfg.RegisterClientConfig).
func RegisterDatabaseClient(i do.Injector) {
	do.Provide(i, func(i do.Injector) (database.Client, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		// Built into a variable and returned only once its error is known to be
		// nil: NewDatabaseClient returns *Client, so returning it straight
		// through would convert a nil pointer into a non-nil database.Client on
		// the error path.
		client, err := NewDatabaseClient(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[database.ClientConfig](i),
			WithLogger(pillars.Logger),
			WithTracerProvider(pillars.TracerProvider),
			WithMetricsProvider(pillars.MetricsProvider),
		)
		if err != nil {
			return nil, err
		}

		return client, nil
	})
}
