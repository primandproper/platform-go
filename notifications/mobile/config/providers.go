package mobilecfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/notifications/mobile"
)

// NewPushSender provides a PushNotificationSender from config.
func NewPushSender(
	ctx context.Context,
	cfg Config,
	opts ...Option,
) (mobile.PushNotificationSender, error) {
	return (&cfg).NewPushSender(ctx, opts...)
}
