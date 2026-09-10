package rbaccfg

import (
	"context"

	"github.com/primandproper/primitives-go/authorization"
	"github.com/primandproper/primitives-go/cache"
	"github.com/primandproper/primitives-go/config/cfgnorm"
	"github.com/primandproper/primitives-go/config/injection"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/observability"

	"github.com/samber/do/v2"
)

// RegisterPolicyResolver registers an authorization.PolicyResolver with the
// injector. The cache is optional: a registered
// cache.Cache[authorization.PermissionSet] wraps the resolver in the cached
// decorator, and its absence means every resolution hits the underlying
// resolver, which is NewPolicyResolver's documented uncached behavior.
//
// It provides the same key as authorizationcfg.RegisterPolicyResolver, so
// exactly one of the two belongs in any given injector: this one where policy
// may come from SQL, that one where it never does.
//
// Prerequisites: context.Context and *Config must be registered in the injector
// before the resolver is invoked. A database.Client is only required when the
// config's provider is "database", so a statically-authorized service can build
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
