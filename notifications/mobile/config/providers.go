package config

import (
	"context"

	"github.com/primandproper/platform-go/v6/notifications/mobile"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"
)

// NewPushSender provides a PushNotificationSender from config.
func NewPushSender(
	ctx context.Context,
	cfg Config,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
) (mobile.PushNotificationSender, error) {
	return (&cfg).NewPushSender(ctx, logger, tracerProvider, metricsProvider)
}
