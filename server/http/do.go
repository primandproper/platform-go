package http

import (
	"context"

	"github.com/primandproper/platform-go/v13/healthcheck"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/routing"

	"github.com/samber/do/v2"
)

// RegisterHTTPServer registers a Server with the injector.
// The serviceName parameter is passed directly rather than injected, since
// string is too generic a type to resolve unambiguously from the injector.
//
// The server it builds serves the operational routes: VersionPath always, and
// the two probe paths when a healthcheck.Registry is registered. This is the
// wire-it-all-up path, so it opts into what a hand-built server is asked to opt
// into — a caller who wants the probes elsewhere, or not at all, calls
// NewHTTPServer with the options it wants instead.
func RegisterHTTPServer(i do.Injector, serviceName string) {
	do.Provide(i, func(i do.Injector) (Server, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		// Optional, because a registry is something the composition root builds
		// from what it registered and a container assembled by hand may have
		// none. Absent mounts no probes; registered-but-unbuildable is an error,
		// since a service that was meant to report its health must not come up
		// silently unable to.
		registry, err := injection.InvokeOptional[healthcheck.Registry](i)
		if err != nil {
			return nil, err
		}

		cfg := do.MustInvoke[Config](i)

		srv, err := NewHTTPServer(
			do.MustInvoke[context.Context](i),
			&cfg,
			do.MustInvoke[*routing.Router](i),
			WithServiceName(serviceName),
			WithLogger(pillars.Logger),
			WithTracerProvider(pillars.TracerProvider),
			WithHealthRegistry(registry),
			WithVersionEndpoint(),
		)
		if err != nil {
			return nil, err
		}

		return srv, nil
	})
}
