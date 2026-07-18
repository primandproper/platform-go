package routingcfg

import (
	"github.com/primandproper/platform-go/v5/encoding"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"
	"github.com/primandproper/platform-go/v5/routing"

	"github.com/samber/do/v2"
)

// RegisterRouter registers a routing.Backend and a *routing.Router with the
// injector, resolving the backend by provider and layering the declarative
// Router (with its encoder) on top.
func RegisterRouter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (routing.Backend, error) {
		return NewBackend(
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})

	do.Provide(i, func(i do.Injector) (*routing.Router, error) {
		return routing.New(
			do.MustInvoke[routing.Backend](i),
			do.MustInvoke[encoding.ServerEncoderDecoder](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
		), nil
	})
}
