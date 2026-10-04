package billingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/billing"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers a billing.Store with the injector.
//
// Prerequisites: *Config, database.Client and billing.Hooks must be registered
// in the injector before the Store is invoked. The hooks are required; a
// container that wants none registers billing.NoopHooks{} by name.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (billing.Store, error) {
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

		hooks, err := do.Invoke[billing.Hooks](i)
		if err != nil {
			return nil, err
		}

		return NewStore(ctx, cfg, client, hooks, WithPillars(pillars))
	})
}
