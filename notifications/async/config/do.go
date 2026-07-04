package asynccfg

import (
	"github.com/primandproper/platform-go/v3/notifications/async"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	"github.com/primandproper/platform-go/v3/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterAsyncNotifier registers an async.AsyncNotifier with the injector.
func RegisterAsyncNotifier(i do.Injector) {
	do.Provide[async.AsyncNotifier](i, func(i do.Injector) (async.AsyncNotifier, error) {
		return ProvideAsyncNotifierFromConfig(
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})
}
