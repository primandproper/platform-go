package service

import (
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/healthcheck"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// The names the auto-wired checkers report under. They are what a /readyz body
// keys its components by, what a grpc_health_v1 Check asks for by name, and
// therefore part of this package's interface rather than labels: renaming one
// changes what an operator's dashboard and a client's health check are asking
// about.
const (
	databaseCheckerName     = "database"
	messageQueueCheckerName = "message_queue"
)

// registerHealth provides the health registry, populated from whatever else the
// injector can build.
//
// This is the half of "health for free" that has to live in the composition
// root. The adapters in healthcheck have always known how to wrap a database
// client or a message queue publisher; what nobody could say from inside those
// packages is which of them a given service is actually made of. Register does
// know, so the registry a service gets already contains its infrastructure and
// the application adds only what the platform cannot see.
//
// It is provided rather than built, so the checks run against the same clients
// the rest of the service uses — do hands out one instance — and so a registry
// nothing serves is never built at all.
//
// A caller that would rather assemble the registry itself calls
// healthcheck.RegisterRegistry after this, which replaces the provider with one
// that starts empty.
func registerHealth(i do.Injector) {
	do.Provide(i, func(i do.Injector) (healthcheck.Registry, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		registry, err := healthcheck.NewRegistry(healthcheck.WithPillars(pillars))
		if err != nil {
			return nil, err
		}

		// Readiness is an optional capability of a Client rather than part of the
		// interface — every provider this module ships has it, and one an
		// application wrote may not. An asserted-away client is left unchecked
		// rather than reported down, and its owner, who is the only one who knows
		// how to ask it, joins a checker through WithHealthChecks.
		if err = adopt(i, registry, func(c database.Client) healthcheck.Checker {
			ready, ok := c.(healthcheck.DatabaseReadyChecker)
			if !ok {
				return nil
			}

			return healthcheck.NewDatabaseChecker(databaseCheckerName, ready)
		}); err != nil {
			return nil, err
		}

		// The publisher provider, because it is the end of the queue a request
		// path depends on: a service that cannot publish cannot serve, while one
		// that cannot consume is behind rather than unready. ConsumerProvider
		// has no Ping to ask anyway.
		if err = adopt(i, registry, func(p messagequeue.PublisherProvider) healthcheck.Checker {
			return healthcheck.NewMessageQueueChecker(messageQueueCheckerName, p)
		}); err != nil {
			return nil, err
		}

		// Caches are the third adapter healthcheck ships and the one that cannot
		// be wired here: cache.Cache[T] is registered per concrete type, under a
		// name that includes a type argument no config can supply, so there is
		// nothing for this walk to look up. An application joins its own with
		// WithHealthChecks(healthcheck.NewCacheChecker("cache", c)), which is the
		// same seam its domain checks arrive through.

		return registry, nil
	})
}

// adopt registers a checker for T, when the injector has a T to wrap and wrap
// knows how to check it.
//
// Absence is not a failure — a subsystem the config never named is a subsystem
// this service is not made of, and a registry that reported it down would fail
// every readiness probe over a component nobody asked for. A subsystem that was
// configured and cannot be built is a different thing entirely, and is returned:
// building the registry is when a service finds out, and reporting healthy
// because the check itself could not be constructed is the one answer worse than
// reporting down.
//
// wrap returning nil means the component is present but cannot be asked, which
// is neither of those cases and contributes nothing.
func adopt[T any](i do.Injector, registry healthcheck.Registry, wrap func(T) healthcheck.Checker) error {
	v, err := injection.InvokeOptional[T](i)
	if err != nil {
		return platformerrors.Wrapf(err, "invoking %s", do.NameOf[T]())
	}

	if isAbsent(v) {
		return nil
	}

	if checker := wrap(v); checker != nil {
		registry.Register(checker)
	}

	return nil
}
