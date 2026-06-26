package config

import (
	"context"

	"github.com/primandproper/platform-go/notifications/mobile"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"
)

// ProvidePushSender provides a PushNotificationSender from config.
func ProvidePushSender(
	ctx context.Context,
	cfg Config,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
) (mobile.PushNotificationSender, error) {
	return (&cfg).ProvidePushSender(ctx, logger, tracerProvider, metricsProvider)
}
