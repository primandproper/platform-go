package issuereportscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/issuereports"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers an issuereports.Store with the injector.
//
// Prerequisites: *Config, database.Client and issuereports.Hooks must be registered in
// the injector before the Store is invoked. A container that wants no hooks
// registers issuereports.NoopHooks{} by name.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (issuereports.Store, error) {
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

		hooks, err := do.Invoke[issuereports.Hooks](i)
		if err != nil {
			return nil, err
		}

		return NewStore(ctx, cfg, client, hooks, WithPillars(pillars))
	})
}
