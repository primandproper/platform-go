package profilingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/profiling"

	"github.com/samber/do/v2"
)

// RegisterProfilingProvider registers a profiling.Provider with the injector.
func RegisterProfilingProvider(i do.Injector) {
	do.Provide[profiling.Provider](i, func(i do.Injector) (profiling.Provider, error) {
		return NewProfilingProvider(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[*Config](i),
		)
	})
}
