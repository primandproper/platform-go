package webhookscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/webhooks"

	"github.com/samber/do/v2"
)

// RegisterStore registers a webhooks.Store with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the
// injector before the Store is invoked.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (webhooks.Store, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewStore(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}

// RegisterDispatcher registers a webhooks.Dispatcher with the injector.
//
// Prerequisites: *Config, webhooks.Store (see RegisterStore), and
// webhooks.Catalog must be registered in the injector before the Dispatcher is
// invoked. The Catalog is the application's declaration of which event types
// exist, so it has no environment-driven construction here.
func RegisterDispatcher(i do.Injector) {
	do.Provide(i, func(i do.Injector) (webhooks.Dispatcher, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewDispatcher(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[webhooks.Store](i),
			do.MustInvoke[webhooks.Catalog](i),
			WithPillars(pillars),
		)
	})
}

// RegisterWorker registers a *webhooks.Worker with the injector.
//
// Prerequisites: *Config and webhooks.Store (see RegisterStore) must be
// registered in the injector before the Worker is invoked.
func RegisterWorker(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*webhooks.Worker, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewWorker(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[webhooks.Store](i),
			WithPillars(pillars),
		)
	})
}
