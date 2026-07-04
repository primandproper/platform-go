package grpc

import (
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/tracing"

	"github.com/samber/do/v2"
	"google.golang.org/grpc"
)

// RegisterGRPCServer registers a *Server with the injector.
// Prerequisites: []grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor,
// and []RegistrationFunc must be registered in the injector before calling this.
func RegisterGRPCServer(i do.Injector) {
	do.Provide[*Server](i, func(i do.Injector) (*Server, error) {
		return NewGRPCServer(
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[[]grpc.UnaryServerInterceptor](i),
			do.MustInvoke[[]grpc.StreamServerInterceptor](i),
			do.MustInvoke[[]RegistrationFunc](i)...,
		)
	})
}
