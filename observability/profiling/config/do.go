package profilingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/profiling"

	"github.com/samber/do/v2"
)

// RegisterProfilingProvider registers a profiling.Provider with the injector.
//
// The logger is looked up optionally rather than through do.MustInvoke: a
// container that registers no logger still gets a profiler, which just says
// nothing about how it was started.
func RegisterProfilingProvider(i do.Injector) {
	do.Provide(i, func(i do.Injector) (profiling.Provider, error) {
		logger, err := injection.InvokeOptional[logging.Logger](i)
		if err != nil {
			return nil, err
		}

		return NewProfilingProvider(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithLogger(logger),
		)
	})
}
