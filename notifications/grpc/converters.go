package grpc

import (
	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	platformerrors "github.com/primandproper/primitives-go/errors"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// NotificationToProto renders one inbox row for the wire.
//
// It carries no scope and no principal, and neither is an omission: the
// notification belongs to the caller in the directory the connection resolved,
// so either field would be telling a client something it supplied. There is
// nowhere on the message to put them — notifications.proto reserves both names.
//
// It is exported because a consumer composing an inbox into a larger response —
// an application assembling a home screen — otherwise writes the same nine
// assignments and gets one of the three nullable times wrong.
func NotificationToProto(n *notifications.Notification) *notificationspb.Notification {
	if n == nil {
		return nil
	}

	out := &notificationspb.Notification{
		CreatedAt: timestamppb.New(n.CreatedAt),
		Id:        n.ID,
		Topic:     n.Topic,
		Title:     n.Title,
		Body:      n.Body,
		Link:      n.Link,
	}

	// The three nullable times stay unset rather than becoming the zero
	// timestamp. A client rendering an unread badge asks whether read_at is
	// there, and 1970 is not the answer "they have not read it".
	if n.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*n.LastUpdatedAt)
	}

	if n.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*n.ArchivedAt)
	}

	if n.ReadAt != nil {
		out.ReadAt = timestamppb.New(*n.ReadAt)
	}

	return out
}

// NotificationsToProto renders a page of inbox rows.
func NotificationsToProto(in []*notifications.Notification) []*notificationspb.Notification {
	out := make([]*notificationspb.Notification, 0, len(in))
	for _, n := range in {
		out = append(out, NotificationToProto(n))
	}

	return out
}

// DeviceToProto renders one registration for the wire.
//
// It carries no device token, and that is the property rather than an
// oversight: notifications.Device holds one, notificationspb.Device has nowhere
// to put one, and the name is reserved on the message. A handset knows its own
// token because it minted it and just sent it; handing the set back the other
// way would make listing devices the call that harvests every push address an
// account holds.
func DeviceToProto(d *notifications.Device) *notificationspb.Device {
	if d == nil {
		return nil
	}

	return &notificationspb.Device{
		CreatedAt:  timestamppb.New(d.CreatedAt),
		LastSeenAt: timestamppb.New(d.LastSeenAt),
		Id:         d.ID,
		Platform:   PlatformToProto(d.Platform),
	}
}

// DevicesToProto renders a page of registrations.
func DevicesToProto(in []*notifications.Device) []*notificationspb.Device {
	out := make([]*notificationspb.Device, 0, len(in))
	for _, d := range in {
		out = append(out, DeviceToProto(d))
	}

	return out
}

// PlatformToProto renders a platform for the wire.
//
// A platform this module does not serve renders as UNSPECIFIED rather than as
// its own name, because there is no name to render it under: the enum is the
// closed set notifications/mobile can route to. A row holding one is a row that
// predates a platform being withdrawn, and a client is better told "not one of
// these" than given a number its generated code has no case for.
func PlatformToProto(p notifications.Platform) notificationspb.DevicePlatform {
	switch p {
	case notifications.PlatformIOS:
		return notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS
	case notifications.PlatformAndroid:
		return notificationspb.DevicePlatform_DEVICE_PLATFORM_ANDROID
	default:
		return notificationspb.DevicePlatform_DEVICE_PLATFORM_UNSPECIFIED
	}
}

// platformFromProto reads a platform off a registration request.
//
// UNSPECIFIED is refused rather than defaulted. proto3's zero value is what a
// client that set no platform sends, and guessing which provider a token
// belongs to is guessing which network a credential is sent to — a token
// registered under the wrong one is a handset that is never pushed to and whose
// row is never pruned, because the feedback that prunes rows comes from the
// provider that rejected them.
//
// The refusal is notifications.ErrUnknownPlatform, which is the package's own
// sentinel for exactly this and is already mapped to a bad request on both
// transports, rather than a second sentinel here saying the same thing in this
// package's words.
func platformFromProto(p notificationspb.DevicePlatform) (notifications.Platform, error) {
	switch p {
	case notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS:
		return notifications.PlatformIOS, nil
	case notificationspb.DevicePlatform_DEVICE_PLATFORM_ANDROID:
		return notifications.PlatformAndroid, nil
	case notificationspb.DevicePlatform_DEVICE_PLATFORM_UNSPECIFIED:
		return "", platformerrors.Wrap(notifications.ErrUnknownPlatform, "the registration names no platform")
	default:
		return "", platformerrors.Wrapf(notifications.ErrUnknownPlatform, "device platform %d", int32(p))
	}
}

// deviceFromProto builds the registration this surface writes.
//
// The principal and the scope come from the caller and are not read off the
// message, which has neither field. The id, the creation time and the last-seen
// time are left unset: the store assigns what a new registration needs and
// keeps what a re-registration already had.
//
// The entity's own Scope is deliberately left empty rather than set from the
// argument. notifications refuses a write whose entity names a different scope
// than the call, and adopts the call's where the entity names none, so leaving
// it empty is naming the scope in exactly one place — which is the whole reason
// that rule exists.
func deviceFromProto(in *notificationspb.DeviceRegistrationInput, principal string) (*notifications.Device, error) {
	if in == nil {
		return nil, ErrNilRegistrationInput
	}

	platform, err := platformFromProto(in.GetPlatform())
	if err != nil {
		return nil, err
	}

	return &notifications.Device{
		Principal: principal,
		Token:     in.GetToken(),
		Platform:  platform,
	}, nil
}
