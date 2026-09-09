package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// TestTheScopeNameIsReserved is what makes "the tenant comes off the connection"
// a property of the schema rather than of this package.
//
// A comment saying a request must not name a scope is a request to the next
// author; `reserved "scope"` is a schema protoc refuses the field into, here and
// in a consumer's fork of the file alike. It is audit/grpc's pattern, adopted
// rather than re-derived.
//
// The three messages a response is built from reserve it too. Every row a
// response returns belongs to the scope the connection resolved, so a scope
// field would tell a client something it supplied — and its absence is what
// makes a converter unable to read one back out of a request.
func TestTheScopeNameIsReserved(T *testing.T) {
	T.Parallel()

	messages := []protoreflect.FullName{
		"primandproper.platform.webhooks.v1.SaveEndpointRequest",
		"primandproper.platform.webhooks.v1.GetEndpointRequest",
		"primandproper.platform.webhooks.v1.ListEndpointsRequest",
		"primandproper.platform.webhooks.v1.ArchiveEndpointRequest",
		"primandproper.platform.webhooks.v1.AddSubscriptionRequest",
		"primandproper.platform.webhooks.v1.GetSubscriptionRequest",
		"primandproper.platform.webhooks.v1.ListSubscriptionsRequest",
		"primandproper.platform.webhooks.v1.ArchiveSubscriptionRequest",
		"primandproper.platform.webhooks.v1.ListAttemptsRequest",
		"primandproper.platform.webhooks.v1.WebhookEndpointInput",
		"primandproper.platform.webhooks.v1.WebhookEndpoint",
		"primandproper.platform.webhooks.v1.WebhookSubscription",
		"primandproper.platform.webhooks.v1.WebhookAttempt",
	}

	for _, name := range messages {
		T.Run(string(name), func(t *testing.T) {
			t.Parallel()

			message := messageNamed(t, name)

			reserved := message.ReservedNames()

			var found bool

			for i := range reserved.Len() {
				if reserved.Get(i) == "scope" {
					found = true
				}
			}

			test.True(t, found, test.Sprintf(
				"%s does not reserve the name \"scope\", so protoc would accept one being added", name))
		})
	}
}

// TestNoResponseMessageCanCarryAKeyring is the structural half of "no endpoint
// signing secret readable through the surface".
//
// The suite's SaveEndpoint tests show the keys do not come back today; this one
// shows there is nowhere for them to come back from, by walking every message in
// the file rather than the ones somebody remembered. A field added to
// WebhookEndpoint in a later revision fails here rather than in an incident.
//
// Two names are excluded and both are the exception itself: the keyring message,
// and the single request that carries one.
func TestNoResponseMessageCanCarryAKeyring(T *testing.T) {
	T.Parallel()

	messages := webhookspb.File_primandproper_platform_webhooks_v1_webhooks_proto.Messages()
	must.Positive(T, messages.Len(), must.Sprint("no messages in the file, so this asserted nothing"))

	for i := range messages.Len() {
		message := messages.Get(i)

		name := string(message.Name())
		if name == "WebhookSigningKeys" || name == "SaveEndpointRequest" {
			continue
		}

		fields := message.Fields()
		for j := range fields.Len() {
			field := fields.Get(j)

			// By type as well as by name, because the leak that matters is a
			// keyring reachable from a response and it would not have to be
			// spelled "signing_keys" to be one.
			if field.Kind() == protoreflect.MessageKind {
				test.NotEq(T, protoreflect.FullName("primandproper.platform.webhooks.v1.WebhookSigningKeys"),
					field.Message().FullName(), test.Sprintf(
						"%s.%s puts a keyring somewhere it can be read back", message.Name(), field.Name()))
			}

			test.False(T, field.Kind() == protoreflect.BytesKind, test.Sprintf(
				"%s.%s is a bytes field outside the one request that carries a credential; "+
					"if it is not a key, give it a type that says so", message.Name(), field.Name()))
		}
	}
}

// TestTheServiceIsNineMethods pins the count the .proto's service comment
// argues for, so that a tenth arrives with a failing test naming the argument
// rather than as a diff nobody weighed against it.
func TestTheServiceIsNineMethods(T *testing.T) {
	T.Parallel()

	methods := webhookspb.File_primandproper_platform_webhooks_v1_webhooks_proto.
		Services().ByName("WebhooksService").Methods()

	test.EqOp(T, 9, methods.Len())
}

func messageNamed(tb testing.TB, name protoreflect.FullName) protoreflect.MessageDescriptor {
	tb.Helper()

	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
	must.NoError(tb, err)

	message, ok := descriptor.(protoreflect.MessageDescriptor)
	must.True(tb, ok)

	return message
}
