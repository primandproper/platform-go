package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// TestTheScopeAndPrincipalNamesAreReserved is what makes "the tenant and the
// recipient come off the connection" a property of the schema rather than of
// this package.
//
// A comment saying a request must not name a principal is a request to the next
// author; `reserved "scope", "principal"` is a schema protoc refuses those
// fields into, here and in a consumer's fork of the file alike. It is
// audit/grpc's pattern for the scope, applied to the second field this surface
// cannot let a caller choose — and on this surface the second is the sharper of
// the two, because it is the whole of the row-level authorization.
//
// The three messages a response is built from reserve both names as well. Every
// row a response returns belongs to the caller in the directory the connection
// resolved, so either field would be telling a client something it supplied.
func TestTheScopeAndPrincipalNamesAreReserved(T *testing.T) {
	T.Parallel()

	messages := []protoreflect.FullName{
		"primandproper.platform.notifications.v1.ListNotificationsRequest",
		"primandproper.platform.notifications.v1.ListUnreadNotificationsRequest",
		"primandproper.platform.notifications.v1.GetNotificationRequest",
		"primandproper.platform.notifications.v1.MarkNotificationReadRequest",
		"primandproper.platform.notifications.v1.MarkAllNotificationsReadRequest",
		"primandproper.platform.notifications.v1.ArchiveNotificationRequest",
		"primandproper.platform.notifications.v1.RegisterDeviceRequest",
		"primandproper.platform.notifications.v1.ListDevicesRequest",
		"primandproper.platform.notifications.v1.RevokeDeviceRequest",
		"primandproper.platform.notifications.v1.Notification",
		"primandproper.platform.notifications.v1.Device",
		"primandproper.platform.notifications.v1.DeviceRegistrationInput",
	}

	for _, name := range messages {
		T.Run(string(name), func(t *testing.T) {
			t.Parallel()

			reserved := reservedNames(messageNamed(t, name))

			for _, field := range []string{"scope", "principal"} {
				test.SliceContains(t, reserved, field, test.Sprintf(
					"%s does not reserve the name %q, so protoc would accept one being added", name, field))
			}
		})
	}
}

// TestNoResponseMessageCanCarryADeviceToken is the structural half of "the token
// travels in one direction".
//
// The suite's registry tests show the token does not come back today; this one
// shows there is nowhere for it to come back from, by walking every message in
// the file rather than the ones somebody remembered. A field added to Device in
// a later revision fails here rather than turning "list my devices" into the
// call that harvests every push address an account holds.
//
// Two messages are excluded and both are the exception itself: the registration
// input, which is where a handset sends the token it minted, and the single
// request that carries one.
func TestNoResponseMessageCanCarryADeviceToken(T *testing.T) {
	T.Parallel()

	messages := notificationspb.File_primandproper_platform_notifications_v1_notifications_proto.Messages()
	must.Positive(T, messages.Len(), must.Sprint("no messages in the file, so this asserted nothing"))

	for i := range messages.Len() {
		message := messages.Get(i)

		if name := message.Name(); name == "DeviceRegistrationInput" || name == "RegisterDeviceRequest" {
			continue
		}

		fields := message.Fields()
		for j := range fields.Len() {
			field := fields.Get(j)

			// By type as well as by name, because a token reachable from a
			// response would not have to be spelled "token" to be one: a nested
			// DeviceRegistrationInput on a response would carry it just as well.
			test.NotEq(T, protoreflect.Name("token"), field.Name(), test.Sprintf(
				"%s.%s puts a device token somewhere it can be read back", message.Name(), field.Name()))

			if field.Kind() == protoreflect.MessageKind {
				test.NotEq(T, protoreflect.FullName("primandproper.platform.notifications.v1.DeviceRegistrationInput"),
					field.Message().FullName(), test.Sprintf(
						"%s.%s puts a registration input somewhere it can be read back",
						message.Name(), field.Name()))
			}
		}
	}
}

// TestTheServiceIsNineMethods pins the count the .proto's service comment argues
// for against the descriptor rather than against the Go stubs, so a tenth
// arrives with a failing test naming the argument.
func TestTheServiceIsNineMethods(T *testing.T) {
	T.Parallel()

	methods := notificationspb.File_primandproper_platform_notifications_v1_notifications_proto.
		Services().ByName("NotificationsService").Methods()

	test.EqOp(T, 9, methods.Len())
}

// TestThePlatformEnumIsTheClosedSetTheModuleServes is the other half of the
// catalog ruling: a topic is the consumer's vocabulary and stays a string, and a
// platform is this module's and is an enum. A third value here would be a
// platform notifications/mobile has no sender for.
func TestThePlatformEnumIsTheClosedSetTheModuleServes(T *testing.T) {
	T.Parallel()

	values := notificationspb.File_primandproper_platform_notifications_v1_notifications_proto.
		Enums().ByName("DevicePlatform").Values()

	must.EqOp(T, 3, values.Len())

	test.EqOp(T, protoreflect.Name("DEVICE_PLATFORM_UNSPECIFIED"), values.Get(0).Name())
	test.EqOp(T, protoreflect.Name("DEVICE_PLATFORM_IOS"), values.Get(1).Name())
	test.EqOp(T, protoreflect.Name("DEVICE_PLATFORM_ANDROID"), values.Get(2).Name())
}

// TestTopicIsAStringAndNotAnEnum is the same ruling read from the other side.
// The catalog of topics is the application's, so putting it in this schema would
// put an application's vocabulary on this module's release cadence.
func TestTopicIsAStringAndNotAnEnum(T *testing.T) {
	T.Parallel()

	topic := messageNamed(T, "primandproper.platform.notifications.v1.Notification").
		Fields().ByName("topic")
	must.NotNil(T, topic)

	test.EqOp(T, protoreflect.StringKind, topic.Kind())
}

func reservedNames(message protoreflect.MessageDescriptor) []string {
	reserved := message.ReservedNames()

	out := make([]string, 0, reserved.Len())
	for i := range reserved.Len() {
		out = append(out, string(reserved.Get(i)))
	}

	return out
}

func messageNamed(tb testing.TB, name protoreflect.FullName) protoreflect.MessageDescriptor {
	tb.Helper()

	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
	must.NoError(tb, err)

	message, ok := descriptor.(protoreflect.MessageDescriptor)
	must.True(tb, ok)

	return message
}
