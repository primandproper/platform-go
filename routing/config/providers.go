package routingcfg

import (
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	"github.com/primandproper/platform-go/v3/observability/tracing"
	"github.com/primandproper/platform-go/v3/routing"
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
