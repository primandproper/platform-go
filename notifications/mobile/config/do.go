package config

import (
	"context"

	"github.com/primandproper/platform-go/v5/notifications/mobile"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterPushSender registers a mobile.PushNotificationSender with the injector.
func RegisterPushSender(i do.Injector) {
	do.Provide[mobile.PushNotificationSender](i, func(i do.Injector) (mobile.PushNotificationSender, error) {
		return NewPushSender(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})
}
