package mobilecfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/notifications/mobile"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterPushSender registers a mobile.PushNotificationSender with the injector.
func RegisterPushSender(i do.Injector) {
	do.Provide(i, func(i do.Injector) (mobile.PushNotificationSender, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewPushSender(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[Config](i),
			WithPillars(pillars),
		)
	})
}
