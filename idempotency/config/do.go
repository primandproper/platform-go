package idempotencycfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterManager registers an *idempotency.Manager[T] with the injector. It
// is generic because a Manager stores results of one concrete type; each
// result type the application replays is registered separately.
//
// Prerequisites: *Config must be registered in the injector before the Manager
// is invoked. A database.Client is only required when the config's lock
// provider is postgres — which is what NewManager documents, and what this
// used to contradict by invoking one unconditionally: a redis-locked container
// that had registered no database panicked at build.
func RegisterManager[T any](i do.Injector) {
	do.Provide(i, func(i do.Injector) (*idempotency.Manager[T], error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		cfg := do.MustInvoke[*Config](i)

		var db database.Client
		if cfg.Lock.RequiresDatabase() {
			client, clientErr := do.Invoke[database.Client](i)
			if clientErr != nil {
				return nil, clientErr
			}
			db = client
		}

		return NewManager[T](
			do.MustInvoke[context.Context](i),
			cfg,
			db,
			WithPillars(pillars),
		)
	})
}
