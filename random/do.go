package random

import (
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterGenerator registers a Generator with the injector.
func RegisterGenerator(i do.Injector) {
	do.Provide[Generator](i, func(i do.Injector) (Generator, error) {
		return NewGenerator(
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
		), nil
	})
}
