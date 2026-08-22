package mobile

import (
	"context"
	"reflect"
	"strings"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
)

// ErrPlatformNotSupported is returned when attempting to send to a platform
// that has no configured sender (e.g., iOS token but APNs not configured).
var ErrPlatformNotSupported = errors.New("push notifications not configured for this platform")

const (
	platformIOS     = "ios"
	platformAndroid = "android"
	o11yName        = "mobile_push_sender"
)

type (
	// APNsSender delivers one notification to an iOS device.
	//
	// It is an interface, not *apns.Sender, so this type has a seam: depending on
	// the concrete struct meant there was no way to test the routing here without
	// a real APNs connection, and no way for a consumer to substitute anything.
	APNsSender interface {
		Send(ctx context.Context, deviceToken, title, body string, badgeCount *int) error
	}

	// FCMSender delivers one notification to an Android device.
	FCMSender interface {
		Send(ctx context.Context, deviceToken, title, body string) error
	}
)

// MultiPlatformPushSender routes push notifications to APNs (iOS) or FCM (Android).
type MultiPlatformPushSender struct {
	o11y       observability.Observer
	apnsSender APNsSender
	fcmSender  FCMSender
}

// NewMultiPlatformPushSender creates a sender that routes by platform.
//
// Either sender may be nil, in which case that platform reports
// ErrPlatformNotSupported rather than silently succeeding.
func NewMultiPlatformPushSender(
	apnsSender APNsSender,
	fcmSender FCMSender,
	opts ...Option,
) *MultiPlatformPushSender {
	o := newOptions(opts)

	return &MultiPlatformPushSender{
		apnsSender: apnsSender,
		fcmSender:  fcmSender,
		o11y:       observability.NewObserver(o11yName, o.logger, o.tracerProvider),
	}
}

// isNil reports whether v is nil, including a typed nil pointer held in an
// interface.
//
// The senders are interfaces so this type has a testable seam, and the config
// path builds `var s *apns.Sender` and passes it unset when that platform is
// not configured — which is a non-nil interface holding a nil pointer. A plain
// `== nil` misses it, and the "platform not configured" branch below then calls
// through the nil pointer instead of reporting ErrPlatformNotSupported.
func isNil(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)

	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}

// SendPush sends a push notification to a single device token, routing by platform.
func (s *MultiPlatformPushSender) SendPush(ctx context.Context, platform, token string, msg PushMessage) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	platform = strings.ToLower(strings.TrimSpace(platform))
	op.Set("platform", platform)

	switch platform {
	case platformIOS:
		if isNil(s.apnsSender) {
			return op.Error(ErrPlatformNotSupported, "sending apns notification")
		}
		return s.apnsSender.Send(ctx, token, msg.Title, msg.Body, msg.BadgeCount)
	case platformAndroid:
		if isNil(s.fcmSender) {
			return op.Error(ErrPlatformNotSupported, "sending fcm notification")
		}
		if msg.BadgeCount != nil {
			// The FCM sender has no badge parameter (Android has no standard OS-level
			// badge in an FCM notification), so BadgeCount is dropped on this path. Log
			// it rather than silently discarding.
			op.Logger().WithValue("badge_count", *msg.BadgeCount).Info("dropping BadgeCount: unsupported on the FCM/Android path")
		}
		return s.fcmSender.Send(ctx, token, msg.Title, msg.Body)
	default:
		return op.Error(errors.Newf("unknown platform %q", platform), "sending apns notification")
	}
}
