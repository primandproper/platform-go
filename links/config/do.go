package linkscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/links"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterMinter registers a *links.Minter with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the injector
// before the Minter is invoked.
func RegisterMinter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*links.Minter, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewMinter(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			WithPillars(pillars),
		)
	})
}
