package config

import (
	"context"

	"github.com/primandproper/platform-go/notifications/mobile"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterPushSender registers a mobile.PushNotificationSender with the injector.
func RegisterPushSender(i do.Injector) {
	do.Provide[mobile.PushNotificationSender](i, func(i do.Injector) (mobile.PushNotificationSender, error) {
		return ProvidePushSender(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})
}
