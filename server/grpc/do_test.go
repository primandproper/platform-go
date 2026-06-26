package grpc

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
)

func TestRegisterGRPCServer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
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
	})
}
