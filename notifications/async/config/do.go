package asynccfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/notifications/async"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterAsyncNotifier registers an async.AsyncNotifier with the injector.
func RegisterAsyncNotifier(i do.Injector) {
	do.Provide(i, func(i do.Injector) (async.AsyncNotifier, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewAsyncNotifier(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}
