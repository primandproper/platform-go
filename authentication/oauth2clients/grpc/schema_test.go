package grpc_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// TestTheScopeNameIsReserved is what makes "the registry comes off the caller's
// principal" a property of the schema rather than of this package.
//
// A comment saying a request must not name a scope is a request to the next
// author; `reserved "scope";` is a schema protoc refuses the field into, here
// and in a consumer's fork of the file alike. It is audit/grpc's pattern,
// adopted rather than re-derived.
//
// It walks every message in the file rather than a list somebody remembered, and
// excludes only the responses — which hold nothing but the messages above, each
// of which reserves the name itself.
func TestTheScopeNameIsReserved(T *testing.T) {
	T.Parallel()

	file := oauth2clientspb.File_primandproper_platform_oauth2clients_v1_oauth2clients_proto
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

// TestTheOAuth2ScopesFieldSurvivesTheReservation is the reason this file needs a
// second test where the rest of the lane needs none.
//
// "scope" is two words here. The reserved one is the tenant, and the field named
// scopes is what a client may ask for at /authorize — an OAuth2 authorization
// scope, which has nothing to do with a tenant and is the whole point of a
// registration. protoc reserves the singular name and leaves the plural alone,
// and this is what notices if somebody "tidies" the reservation into a prefix
// match and takes the registry's own vocabulary out with it.
func TestTheOAuth2ScopesFieldSurvivesTheReservation(T *testing.T) {
	T.Parallel()

	for _, name := range []protoreflect.FullName{
		"primandproper.platform.oauth2clients.v1.OAuth2Client",
		"primandproper.platform.oauth2clients.v1.OAuth2ClientCreationInput",
	} {
		T.Run(string(name), func(t *testing.T) {
			t.Parallel()

			message := messageNamed(t, name)

			test.NotNil(t, message.Fields().ByName("scopes"), test.Sprintf(
				"%s has no scopes field, so a registration cannot say what it may ask for", name))
		})
	}
}

// TestNoMessageCarriesAScope is the other half, and the one that says the field
// is not there today rather than that it cannot be added.
//
// The two are deliberately redundant: a reservation only refuses the name it
// names, and a scope smuggled in as "tenant" or "directory" would satisfy it.
// belongs_to_user is on the same list one level in — an owner a client could
// name is a credential minted in somebody else's name — and it is excluded on
// the two messages a response is built from, where it is output only.
func TestNoMessageCarriesAScope(T *testing.T) {
	T.Parallel()

	forbidden := []string{"scope", "tenant", "tenant_id", "owner", "directory", "belongs_to_user"}

	outputOnly := []string{"OAuth2Client", "IssuedOAuth2Client"}

	messages := oauth2clientspb.File_primandproper_platform_oauth2clients_v1_oauth2clients_proto.Messages()
	must.Positive(T, messages.Len(), must.Sprint("no messages in the file, so this asserted nothing"))

	for i := range messages.Len() {
		message := messages.Get(i)

		name := string(message.Name())
		if slices.Contains(outputOnly, name) {
			continue
		}

		T.Run(name, func(t *testing.T) {
			t.Parallel()

			fields := message.Fields()
			for j := range fields.Len() {
				test.SliceNotContains(t, forbidden, string(fields.Get(j).Name()), test.Sprintf(
					"%s.%s lets a client name something the service decides", name, fields.Get(j).Name()))
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

func messageNamed(tb testing.TB, name protoreflect.FullName) protoreflect.MessageDescriptor {
	tb.Helper()

	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
	must.NoError(tb, err)

	message, ok := descriptor.(protoreflect.MessageDescriptor)
	must.True(tb, ok)

	return message
}
