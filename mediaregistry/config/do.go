package mediaregistrycfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/mediaregistry"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers a mediaregistry.Store with the injector.
//
// Prerequisites: *Config and database.Client must be registered in the injector
// before the Store is invoked.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (mediaregistry.Store, error) {
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
