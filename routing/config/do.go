package routingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/routing"

	"github.com/samber/do/v2"
)

// RegisterRouter registers a routing.Backend and a *routing.Router with the
// injector, resolving the backend by provider and layering the declarative
// Router (with its encoder) on top.
func RegisterRouter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (routing.Backend, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewBackend(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})

	do.Provide(i, func(i do.Injector) (*routing.Router, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return routing.New(
			do.MustInvoke[routing.Backend](i),
			do.MustInvoke[encoding.ServerEncoderDecoder](i),
			routing.WithLogger(pillars.Logger),
			routing.WithTracerProvider(pillars.TracerProvider),
		), nil
	})
}
