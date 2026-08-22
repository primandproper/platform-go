package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v13/healthcheck"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
	"google.golang.org/grpc"
)

// RegisterGRPCServer registers a *Server with the injector.
// Prerequisites: []grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor,
// and []RegistrationFunc must be registered in the injector before calling this.
//
// The server it builds serves grpc_health_v1 when a healthcheck.Registry is
// registered, from the same registry the HTTP server answers /readyz from.
func RegisterGRPCServer(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*Server, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		// Optional, because a registry is something the composition root builds
		// from what it registered and a container assembled by hand may have
		// none. Absent registers no health service; registered-but-unbuildable
		// is an error, since a service that was meant to report its health must
		// not come up silently unable to.
		registry, err := injection.InvokeOptional[healthcheck.Registry](i)
		if err != nil {
			return nil, err
		}

		return NewGRPCServer(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[[]grpc.UnaryServerInterceptor](i),
			do.MustInvoke[[]grpc.StreamServerInterceptor](i),
			do.MustInvoke[[]RegistrationFunc](i),
			WithLogger(pillars.Logger),
			WithTracerProvider(pillars.TracerProvider),
			WithHealthRegistry(registry),
		)
	})
}
