package grpc_test

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestTheScopeNameIsReserved is what makes "the tenant never comes off a request
// field" a property of the schema rather than of this package.
//
// A comment saying a request must not name a scope is a request to the next
// author; `reserved "scope";` is a schema protoc refuses the field into, here
// and in a consumer's fork of the file alike. It is audit/grpc's pattern,
// adopted rather than re-derived.
//
// It matters more on this surface than on any other. Everywhere else the scope
// is resolved from a caller an interceptor has already authenticated; sign-in is
// the one place it cannot be, because the caller has not proved they are anybody
// yet, so it comes off the connection instead. A scope field here would be one
// an anonymous request fills in.
//
// It walks every message in the file rather than a list somebody remembered, and
// excludes only the responses — which hold nothing but the messages above, each
// of which reserves the name itself.
func TestTheScopeNameIsReserved(T *testing.T) {
	T.Parallel()

	file := signinpb.File_primandproper_platform_signin_v1_signin_proto
	must.NotNil(T, file)

	messages := file.Messages()
	must.Positive(T, messages.Len(), must.Sprint("no messages in the file, so this asserted nothing"))

	for i := range messages.Len() {
		message := messages.Get(i)

		name := string(message.Name())
		if strings.HasSuffix(name, "Response") {
			continue
		}

		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.True(t, reserves(message, "scope"), test.Sprintf(
				"%s does not reserve the name \"scope\", so protoc would accept one being added", name))
		})
	}
}

// TestNoMessageCarriesAScope is the other half, and the one that says the field
// is not there today rather than that it cannot be added.
//
// The two are deliberately redundant: a reservation only refuses the name it
// names, and a scope smuggled in as "tenant" or "directory" would satisfy it.
func TestNoMessageCarriesAScope(T *testing.T) {
	T.Parallel()

	forbidden := []string{"scope", "tenant", "tenant_id", "owner", "directory"}

	messages := signinpb.File_primandproper_platform_signin_v1_signin_proto.Messages()
	must.Positive(T, messages.Len(), must.Sprint("no messages in the file, so this asserted nothing"))

	for i := range messages.Len() {
		message := messages.Get(i)

		T.Run(string(message.Name()), func(t *testing.T) {
			t.Parallel()

			fields := message.Fields()
			for j := range fields.Len() {
				test.SliceNotContains(t, forbidden, string(fields.Get(j).Name()), test.Sprintf(
					"%s.%s lets a client name the tenant its request is answered in", message.Name(), fields.Get(j).Name()))
			}
		})
	}
}

// reserves reports whether a message reserves the given field name.
func reserves(message protoreflect.MessageDescriptor, name protoreflect.Name) bool {
	reserved := message.ReservedNames()

	for i := range reserved.Len() {
		if reserved.Get(i) == name {
			return true
		}
	}

	return false
}
