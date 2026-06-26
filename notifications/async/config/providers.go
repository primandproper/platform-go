package asynccfg

import (
	"github.com/primandproper/platform-go/notifications/async"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"
)

// ProvideAsyncNotifierFromConfig provides an AsyncNotifier from a config.
func ProvideAsyncNotifierFromConfig(cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (async.AsyncNotifier, error) {
	return cfg.ProvideAsyncNotifier(logger, tracerProvider, metricsProvider)
}
