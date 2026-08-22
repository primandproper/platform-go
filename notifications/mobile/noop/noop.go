// Package noop is the mobile.PushNotificationSender for a deployment with no
// APNs or FCM credentials. SendPush returns nil for every device token, so no
// notification reaches a handset.
//
// What is lost with it is not only the delivery but the feedback. A real sender
// is how a service learns that a device token is invalid or expired, and that
// signal is what keeps a device registry from filling with dead rows; here
// every token looks permanently good. It is the honest choice for a platform
// that has not integrated push yet, and for tests that exercise the send path.
//
// notifications/mobile/config builds it for the "noop" provider name.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/notifications/mobile"
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
