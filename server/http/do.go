package http

import (
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"
	"github.com/primandproper/platform-go/routing"

	"github.com/samber/do/v2"
)

// RegisterHTTPServer registers a Server with the injector.
// The serviceName parameter is passed directly rather than injected, since
// string is too generic a type to resolve unambiguously from the injector.
func RegisterHTTPServer(i do.Injector, serviceName string) {
	do.Provide[Server](i, func(i do.Injector) (Server, error) {
		return ProvideHTTPServer(
			do.MustInvoke[Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[routing.Router](i),
			do.MustInvoke[tracing.TracerProvider](i),
			serviceName,
		)
	})
}
