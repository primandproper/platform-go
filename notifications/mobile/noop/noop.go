package noop

import (
	"context"

	"github.com/primandproper/platform-go/v6/notifications/mobile"
)

var _ mobile.PushNotificationSender = (*pushNotificationSender)(nil)

// pushNotificationSender is a no-op implementation of PushNotificationSender.
// It does not send any push notifications; used when APNs/FCM is not yet integrated.
type pushNotificationSender struct{}

// SendPush is a no-op; it does not send any notifications.
func (n *pushNotificationSender) SendPush(_ context.Context, _, _ string, _ mobile.PushMessage) error {
	return nil
}

// NewPushNotificationSender returns a no-op PushNotificationSender.
func NewPushNotificationSender() mobile.PushNotificationSender {
	return &pushNotificationSender{}
}
