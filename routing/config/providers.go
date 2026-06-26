package routingcfg

import (
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"
	"github.com/primandproper/platform-go/routing"
)

// ProvideRouterViaConfig provides a Router from config.
func ProvideRouterViaConfig(
	cfg *Config,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricProvider metrics.Provider,
) (routing.Router, error) {
	return cfg.ProvideRouter(logger, tracerProvider, metricProvider)
}
