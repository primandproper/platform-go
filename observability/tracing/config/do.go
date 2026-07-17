package tracingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterTracerProvider registers a tracing.TracerProvider with the injector.
func RegisterTracerProvider(i do.Injector) {
	do.Provide[tracing.TracerProvider](i, func(i do.Injector) (tracing.TracerProvider, error) {
		return NewTracerProvider(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
		)
	})
}
