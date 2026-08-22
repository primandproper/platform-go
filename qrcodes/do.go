package qrcodes

import (
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterBuilder registers a Builder with the injector.
func RegisterBuilder(i do.Injector) {
	do.Provide(i, func(i do.Injector) (Builder, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewBuilder(
			do.MustInvoke[Issuer](i),
			WithLogger(pillars.Logger),
			WithTracerProvider(pillars.TracerProvider),
		), nil
	})
}
