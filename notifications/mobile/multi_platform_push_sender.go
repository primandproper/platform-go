package mobile

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/notifications/mobile/apns"
	"github.com/primandproper/platform-go/notifications/mobile/fcm"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"
)

// ErrPlatformNotSupported is returned when attempting to send to a platform
// that has no configured sender (e.g., iOS token but APNs not configured).
var ErrPlatformNotSupported = errors.New("push notifications not configured for this platform")

const (
	platformIOS     = "ios"
	platformAndroid = "android"
	o11yName        = "mobile_push_sender"
)

// MultiPlatformPushSender routes push notifications to APNs (iOS) or FCM (Android).
type MultiPlatformPushSender struct {
	tracer     tracing.Tracer
	apnsSender *apns.Sender
	fcmSender  *fcm.Sender
	logger     logging.Logger
}

// NewMultiPlatformPushSender creates a sender that routes by platform.
func NewMultiPlatformPushSender(
	apnsSender *apns.Sender,
	fcmSender *fcm.Sender,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
) *MultiPlatformPushSender {
	return &MultiPlatformPushSender{
		apnsSender: apnsSender,
		fcmSender:  fcmSender,
		tracer:     tracing.NewNamedTracer(tracerProvider, o11yName),
		logger:     logging.NewNamedLogger(logger, o11yName),
	}
}

// SendPush sends a push notification to a single device token, routing by platform.
func (s *MultiPlatformPushSender) SendPush(ctx context.Context, platform, token string, msg PushMessage) error {
	ctx, span := s.tracer.StartSpan(ctx)
	defer span.End()

	platform = strings.ToLower(strings.TrimSpace(platform))
	logger := s.logger.WithValue("platform", platform)

	switch platform {
	case platformIOS:
		if s.apnsSender == nil {
			return observability.PrepareAndLogError(ErrPlatformNotSupported, logger, span, "sending apns notification")
		}
		return s.apnsSender.Send(ctx, token, msg.Title, msg.Body, msg.BadgeCount)
	case platformAndroid:
		if s.fcmSender == nil {
			return observability.PrepareAndLogError(ErrPlatformNotSupported, logger, span, "sending apns notification")
		}
		return s.fcmSender.Send(ctx, token, msg.Title, msg.Body)
	default:
		return observability.PrepareAndLogError(errors.Newf("unknown platform %q", platform), logger, span, "sending apns notification")
	}
}
