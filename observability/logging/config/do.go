package loggingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v4/observability/logging"

	"github.com/samber/do/v2"
)

// RegisterLogger registers a logging.Logger with the injector.
func RegisterLogger(i do.Injector) {
	do.Provide[logging.Logger](i, func(i do.Injector) (logging.Logger, error) {
		return NewLogger(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
		)
	})
}
