package commentscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/comments"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

// RegisterStore registers a comments.Store with the injector.
//
// Prerequisites: *Config, database.Client, and comments.Targets must be
// registered in the injector before the Store is invoked. The Targets value is
// the application's declaration of what can be commented on — a set of types,
// each optionally carrying a function that reads the application's own tables —
// so it has no environment-driven construction here.
func RegisterStore(i do.Injector) {
	do.Provide(i, func(i do.Injector) (comments.Store, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewStore(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[database.Client](i),
			do.MustInvoke[comments.Targets](i),
			WithPillars(pillars),
		)
	})
}
