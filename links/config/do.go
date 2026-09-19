package linkscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/links"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterMinter registers a *links.Minter with the injector.
//
// Prerequisites: context.Context, *Config and a database.Client must all be
// registered. The client used to be optional, because a container on the cache
// provider needed none; records live in a table now, so a missing client is a
// wiring failure rather than a different provider.
func RegisterMinter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*links.Minter, error) {
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

		return NewMinter(ctx, cfg, client, WithPillars(pillars))
	})
}
