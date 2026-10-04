package waitlistscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/waitlists"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers a waitlists.Store with the injector.
//
// Prerequisites: *Config, database.Client and waitlists.Hooks must be registered in
// the injector before the Store is invoked. A container that wants no hooks
// registers waitlists.NoopHooks{} by name.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (waitlists.Store, error) {
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

		hooks, err := do.Invoke[waitlists.Hooks](i)
		if err != nil {
			return nil, err
		}

		return NewStore(ctx, cfg, client, hooks, WithPillars(pillars))
	})
}
