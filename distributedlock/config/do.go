package distributedlockcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterLocker registers a distributedlock.Locker with the injector.
//
// Prerequisites: *Config must be registered in the injector before the Locker
// is invoked. A database.Client is only required when the config's provider is
// postgres, so a redis- or memory-locked service can build without one.
func RegisterLocker(i do.Injector) {
	do.Provide(i, func(i do.Injector) (distributedlock.Locker, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		cfg := do.MustInvoke[*Config](i)

		var db database.Client
		if cfg.RequiresDatabase() {
			client, clientErr := do.Invoke[database.Client](i)
			if clientErr != nil {
				return nil, clientErr
			}
			db = client
		}

		return NewLocker(
			do.MustInvoke[context.Context](i),
			cfg,
			db,
			WithPillars(pillars),
		)
	})
}

// RegisterScopedLocker registers a distributedlock.ScopedLocker with the
// injector.
//
// Prerequisites: *Config must be registered in the injector before the Locker
// is invoked. A database.Client is only required when the config's provider is
// postgres, so a redis- or memory-locked service can build without one.
func RegisterScopedLocker(i do.Injector) {
	do.Provide(i, func(i do.Injector) (distributedlock.ScopedLocker, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		cfg := do.MustInvoke[*Config](i)

		var db database.Client
		if cfg.RequiresDatabase() {
			client, clientErr := do.Invoke[database.Client](i)
			if clientErr != nil {
				return nil, clientErr
			}
			db = client
		}

		return NewScopedLocker(
			do.MustInvoke[context.Context](i),
			cfg,
			db,
			WithPillars(pillars),
		)
	})
}
