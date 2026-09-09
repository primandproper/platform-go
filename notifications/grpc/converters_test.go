package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/notifications"
	notificationsgrpc "github.com/primandproper/platform-go/v14/notifications/grpc"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var convertedAt = time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

func TestNotificationToProtoCarriesWhatAClientRenders(T *testing.T) {
	T.Parallel()

	read := convertedAt.Add(time.Hour)

	out := notificationsgrpc.NotificationToProto(&notifications.Notification{
		CreatedAt:     convertedAt,
		LastUpdatedAt: &read,
		ReadAt:        &read,
		ID:            "notif_1",
		Principal:     testPrincipal,
		Topic:         "order.shipped",
		Title:         "your order shipped",
		Body:          "it is on its way",
		Link:          "/orders/1",
		Scope:         testScope,
	})
	must.NotNil(T, out)

	test.EqOp(T, "notif_1", out.GetId())
	test.EqOp(T, "order.shipped", out.GetTopic())
	test.EqOp(T, "your order shipped", out.GetTitle())
	test.EqOp(T, "it is on its way", out.GetBody())
	test.EqOp(T, "/orders/1", out.GetLink())
	test.EqOp(T, convertedAt, out.GetCreatedAt().AsTime())
	test.EqOp(T, read, out.GetReadAt().AsTime())

	// Neither the scope nor the principal came across, and there is nowhere on
	// the message for them to — see schema_test.go, which holds that as a
	// property of the schema rather than of this function.
	test.Nil(T, out.GetArchivedAt())
}

// TestTheNullableTimesStayUnset is the difference between "they have not read
// it" and "they read it in 1970".
func TestTheNullableTimesStayUnset(T *testing.T) {
	T.Parallel()

	out := notificationsgrpc.NotificationToProto(&notifications.Notification{
		CreatedAt: convertedAt,
		ID:        "notif_1",
	})
	must.NotNil(T, out)

	test.Nil(T, out.GetReadAt())
	test.Nil(T, out.GetArchivedAt())
	test.Nil(T, out.GetLastUpdatedAt())
}

// TestDeviceToProtoDropsTheToken is the structural claim held at the converter,
// where a reader of this package meets it. schema_test.go holds the other half:
// that there is nowhere for a token to go.
func TestDeviceToProtoDropsTheToken(T *testing.T) {
	T.Parallel()

	out := notificationsgrpc.DeviceToProto(&notifications.Device{
		CreatedAt:  convertedAt,
		LastSeenAt: convertedAt.Add(time.Hour),
		ID:         "device_1",
		Principal:  testPrincipal,
		Token:      testToken,
		Platform:   notifications.PlatformAndroid,
		Scope:      tenancy.Global(),
	})
	must.NotNil(T, out)

	test.EqOp(T, "device_1", out.GetId())
	test.EqOp(T, notificationspb.DevicePlatform_DEVICE_PLATFORM_ANDROID, out.GetPlatform())
	test.EqOp(T, convertedAt, out.GetCreatedAt().AsTime())

	// The token is the point of this test, and the only way to assert its
	// absence is over the message's own fields.
	for i := range out.ProtoReflect().Descriptor().Fields().Len() {
		field := out.ProtoReflect().Descriptor().Fields().Get(i)
		test.NotEq(T, "token", string(field.Name()))
	}
}

func TestNilConvertsToNil(T *testing.T) {
	T.Parallel()

	test.Nil(T, notificationsgrpc.NotificationToProto(nil))
	test.Nil(T, notificationsgrpc.DeviceToProto(nil))
}

func TestSliceConvertersAreEmptyRatherThanNil(T *testing.T) {
	T.Parallel()

	// An empty page is an empty list on the wire rather than a null, which is
	// what a client iterating the results does not have to guard.
	test.SliceEmpty(T, notificationsgrpc.NotificationsToProto(nil))
	test.SliceEmpty(T, notificationsgrpc.DevicesToProto(nil))

	test.SliceLen(T, 2, notificationsgrpc.NotificationsToProto([]*notifications.Notification{
		{ID: "a"}, {ID: "b"},
	}))
	test.SliceLen(T, 2, notificationsgrpc.DevicesToProto([]*notifications.Device{
		{ID: "a"}, {ID: "b"},
	}))
}

// TestPlatformToProtoRendersUnspecifiedForWhatItCannotName is the honest answer
// for a row holding a platform this module no longer serves: a client's
// generated code has a case for UNSPECIFIED and has none for a number.
func TestPlatformToProtoRendersUnspecifiedForWhatItCannotName(T *testing.T) {
	T.Parallel()

	test.EqOp(T, notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS,
		notificationsgrpc.PlatformToProto(notifications.PlatformIOS))
	test.EqOp(T, notificationspb.DevicePlatform_DEVICE_PLATFORM_ANDROID,
		notificationsgrpc.PlatformToProto(notifications.PlatformAndroid))
	test.EqOp(T, notificationspb.DevicePlatform_DEVICE_PLATFORM_UNSPECIFIED,
		notificationsgrpc.PlatformToProto(notifications.Platform("blackberry")))
}
