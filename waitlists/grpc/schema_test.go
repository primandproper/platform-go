package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// TestTheScopeNameIsReservedEverywhere is what makes "the tenant never comes off
// a request field" a property of the schema rather than of this package.
//
// A comment saying a request must not name a scope is a request to the next
// author; `reserved "scope"` is a schema protoc refuses the field into, here and
// in a consumer's fork of the file alike. It is audit/grpc's pattern, adopted
// rather than re-derived.
//
// It walks every message in the file rather than a list somebody remembered, and
// excludes only the responses — which carry no fields of their own beyond the
// messages above, and whose contents are reserved where those messages are.
func TestTheScopeNameIsReservedEverywhere(T *testing.T) {
	T.Parallel()

	messages := waitlistspb.File_primandproper_platform_waitlists_v1_waitlists_proto.Messages()
	must.Positive(T, messages.Len(), must.Sprint("no messages in the file, so this asserted nothing"))

	for i := range messages.Len() {
		message := messages.Get(i)

		name := string(message.Name())
		if isResponse(name) {
			continue
		}

		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.True(t, reserves(message, "scope"), test.Sprintf(
				"%s does not reserve the name \"scope\", so protoc would accept one being added", name))
		})
	}
}

// TestTheDigestNameIsReservedOnASignup is the structural half of "no contact
// digest readable through the surface".
//
// The suite's reads show the digest does not come back today; this shows there
// is nowhere for it to come back from. It matters because the digest is
// deliberately unsalted over a fast hash — the store's own documentation says it
// is not there to make a withdrawal secret — so a client holding one could test
// any address it liked against it offline.
func TestTheDigestNameIsReservedOnASignup(T *testing.T) {
	T.Parallel()

	signup := messageNamed(T, "primandproper.platform.waitlists.v1.Signup")

	test.True(T, reserves(signup, "contact_digest"), test.Sprint(
		"Signup does not reserve \"contact_digest\", so protoc would accept one being added"))
}

// TestJoinReservesTheProvenanceFields is the structural half of "a signup's
// subject comes off the caller".
//
// A subject a client could name is a signup attributed to somebody who did not
// make it, and notes is the operator's column — UpdateSignupNotes is behind a
// grant, and a form that could write it would be writing past that grant. Both
// absences are the schema's rather than the converter's.
func TestJoinReservesTheProvenanceFields(T *testing.T) {
	T.Parallel()

	join := messageNamed(T, "primandproper.platform.waitlists.v1.JoinRequest")

	test.True(T, reserves(join, "subject"), test.Sprint(
		"JoinRequest does not reserve \"subject\", so a signup could be attributed to somebody who did not make it"))
	test.True(T, reserves(join, "notes"), test.Sprint(
		"JoinRequest does not reserve \"notes\", so a form could write the operator's column"))
}

// TestTheServiceIsSeventeenMethods pins the count the .proto's service comment
// argues for, so that an eighteenth arrives with a failing test naming the
// argument rather than as a diff nobody weighed against it.
//
// Seventeen is every method of waitlists.Store, which is unusual on this lane;
// roster_test.go is where that correspondence is checked rather than counted.
func TestTheServiceIsSeventeenMethods(T *testing.T) {
	T.Parallel()

	methods := waitlistspb.File_primandproper_platform_waitlists_v1_waitlists_proto.
		Services().ByName("WaitlistsService").Methods()

	test.EqOp(T, 17, methods.Len())
}

// isResponse reports whether a message is one of the service's responses, which
// are the messages the reservation does not reach.
func isResponse(name string) bool {
	return len(name) > len("Response") && name[len(name)-len("Response"):] == "Response"
}

// reserves reports whether a message reserves the given field name.
func reserves(message protoreflect.MessageDescriptor, name string) bool {
	reserved := message.ReservedNames()

	for i := range reserved.Len() {
		if string(reserved.Get(i)) == name {
			return true
		}
	}

	return false
}

func messageNamed(tb testing.TB, name protoreflect.FullName) protoreflect.MessageDescriptor {
	tb.Helper()

	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
	must.NoError(tb, err)

	message, ok := descriptor.(protoreflect.MessageDescriptor)
	must.True(tb, ok)

	return message
}
