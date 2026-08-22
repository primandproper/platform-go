package grpc

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/healthcheck"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func TestRegisterGRPCServer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Port: 0})
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, []grpc.UnaryServerInterceptor(nil))
		do.ProvideValue(i, []grpc.StreamServerInterceptor(nil))
		do.ProvideValue(i, []RegistrationFunc(nil))

		RegisterGRPCServer(i)

		srv, err := do.Invoke[*Server](i)
		must.NoError(t, err)
		test.NotNil(t, srv)

		// Nothing registered a registry, so there is no health service to
		// answer from — the bridge stays usable from a container the
		// composition root never touched.
		_, registered := srv.grpcServer.GetServiceInfo()[grpc_health_v1.Health_ServiceDesc.ServiceName]
		test.False(t, registered)
	})

	T.Run("serves health from the registry the container carries", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Port: 0})
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, []grpc.UnaryServerInterceptor(nil))
		do.ProvideValue(i, []grpc.StreamServerInterceptor(nil))
		do.ProvideValue(i, []RegistrationFunc(nil))
		do.ProvideValue[healthcheck.Registry](i, newHealthRegistry(t))

		RegisterGRPCServer(i)

		srv, err := do.Invoke[*Server](i)
		must.NoError(t, err)

		_, registered := srv.grpcServer.GetServiceInfo()[grpc_health_v1.Health_ServiceDesc.ServiceName]
		test.True(t, registered)
	})

	T.Run("a registry that cannot be built is an error", func(t *testing.T) {
		t.Parallel()

		// A service meant to report its health must not come up silently
		// unable to.
		errBuild := errors.New("building the registry")

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Port: 0})
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, []grpc.UnaryServerInterceptor(nil))
		do.ProvideValue(i, []grpc.StreamServerInterceptor(nil))
		do.ProvideValue(i, []RegistrationFunc(nil))
		do.Provide(i, func(do.Injector) (healthcheck.Registry, error) { return nil, errBuild })

		RegisterGRPCServer(i)

		_, err := do.Invoke[*Server](i)
		must.Error(t, err)
		test.ErrorIs(t, err, errBuild)
	})
}
