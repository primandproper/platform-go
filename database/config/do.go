package databasecfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterClientConfig registers a database.ClientConfig with the injector.
func RegisterClientConfig(i do.Injector) {
	do.Provide(i, func(i do.Injector) (database.ClientConfig, error) {
		cfg := do.MustInvoke[*Config](i)
		return NewClientConfig(*cfg), nil
	})
}

// RegisterDatabase registers a database.Client with the injector.
// Prerequisite: *Config must be registered in the injector. A database.Migrator is
// only required when the config's RunMigrations is true.
func RegisterDatabase(i do.Injector) {
	RegisterClientConfig(i)
	do.Provide(i, func(i do.Injector) (database.Client, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		cfg := do.MustInvoke[*Config](i)

		// Only require a Migrator when migrations are actually enabled, so a service
		// that doesn't run migrations can build without registering one.
		var migrator database.Migrator
		if cfg.RunMigrations {
			m, migratorErr := do.Invoke[database.Migrator](i)
			if migratorErr != nil {
				return nil, migratorErr
			}
			migrator = m
		}

		return NewDatabase(
			do.MustInvoke[context.Context](i),
			cfg,
			migrator,
			WithPillars(pillars),
		)
	})
}
