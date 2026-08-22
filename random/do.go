package random

import (
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterGenerator registers a Generator with the injector.
func RegisterGenerator(i do.Injector) {
	do.Provide(i, func(i do.Injector) (Generator, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewGenerator(
			WithLogger(pillars.Logger),
			WithTracerProvider(pillars.TracerProvider),
		), nil
	})
}
