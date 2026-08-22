package authorizationcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/authorization"
	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterPolicyResolver registers an authorization.PolicyResolver with the
// injector. The cache is optional: a registered
// cache.Cache[authorization.PermissionSet] wraps the resolver in the cached
// decorator, and its absence means every resolution hits the underlying
// resolver, which is NewPolicyResolver's documented uncached behavior.
//
// Prerequisites: *Config must be registered in the injector before the
// resolver is invoked. A database.Client is only required when the config's
// provider is "database", so a statically-authorized service can build
// without one.
func RegisterPolicyResolver(i do.Injector) {
	do.Provide(i, func(i do.Injector) (authorization.PolicyResolver, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		cfg := do.MustInvoke[*Config](i)

		// The database resolver both reads and archives roles, so it gets the
		// writer rather than a read replica that would reject its mutations.
		var db database.SQLQueryExecutor
		if cfgnorm.Provider(cfg.Provider) == ProviderDatabase {
			client, clientErr := do.Invoke[database.Client](i)
			if clientErr != nil {
				return nil, clientErr
			}
			db = client.Writer()
		}

		permissionSets, err := injection.InvokeOptional[cache.Cache[authorization.PermissionSet]](i)
		if err != nil {
			return nil, err
		}

		return NewPolicyResolver(
			do.MustInvoke[context.Context](i),
			cfg,
			db,
			permissionSets,
			WithPillars(pillars),
		)
	})
}
