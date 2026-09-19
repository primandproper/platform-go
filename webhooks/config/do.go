package webhookscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

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

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		return NewStore(ctx, cfg, client, WithPillars(pillars))
	})
}

// RegisterDispatcher registers a webhooks.Dispatcher with the injector.
//
// Prerequisites: *Config, database.Client, webhooks.Store (see RegisterStore),
// and webhooks.Catalog must be registered in the injector before the Dispatcher
// is invoked. The Catalog is the application's declaration of which event types
// exist, so it has no environment-driven construction here. The client is the
// one NewDispatcher takes a reader off; see there for the single read it serves.
func RegisterDispatcher(i do.Injector) {
	do.Provide(i, func(i do.Injector) (webhooks.Dispatcher, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		store, err := do.Invoke[webhooks.Store](i)
		if err != nil {
			return nil, err
		}

		catalog, err := do.Invoke[webhooks.Catalog](i)
		if err != nil {
			return nil, err
		}

		return NewDispatcher(ctx, cfg, client, store, catalog, WithPillars(pillars))
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

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		store, err := do.Invoke[webhooks.Store](i)
		if err != nil {
			return nil, err
		}

		return NewWorker(ctx, cfg, store, WithPillars(pillars))
	})
}
