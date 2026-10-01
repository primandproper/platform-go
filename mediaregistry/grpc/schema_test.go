package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/mediaregistry/mediaregistrypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestTheScopeNameIsReservedEverywhere is what makes "the tenant comes off the
// caller" a property of the schema rather than of this package.
func TestTheScopeNameIsReservedEverywhere(T *testing.T) {
	T.Parallel()

	messages := mediaregistrypb.File_primandproper_platform_mediaregistry_v1_mediaregistry_proto.Messages()
	must.Positive(T, messages.Len(), must.Sprint("no messages in the file, so this asserted nothing"))

	for i := range messages.Len() {
		message := messages.Get(i)

		T.Run(string(message.Name()), func(t *testing.T) {
			t.Parallel()

			test.True(t, reserves(message, "scope"), test.Sprintf(
				"%s does not reserve the name \"scope\", so protoc would accept one being added", message.Name()))
		})
	}
}

// TestNoWriteCanCarryAnOwner is the structural half of "an object belongs to
// whoever sent it": there is nowhere in a write for a different owner to go.
func TestNoWriteCanCarryAnOwner(T *testing.T) {
	T.Parallel()

	for _, name := range []protoreflect.Name{"UploadObjectHeader", "RecordObjectRequest", "ListMyObjectsRequest"} {
		T.Run(string(name), func(t *testing.T) {
			t.Parallel()

			message := messageNamed(t, name)

			test.True(t, reserves(message, "owner_id"), test.Sprintf("%s does not reserve \"owner_id\"", name))
			test.Nil(t, message.Fields().ByName("owner_id"))
		})
	}
}

// TestAnUploadCannotNameWhereItsBytesGo is the key layout as a schema fact: a
// key a client could name is a client choosing whose object it overwrites.
func TestAnUploadCannotNameWhereItsBytesGo(T *testing.T) {
	T.Parallel()

	header := messageNamed(T, "UploadObjectHeader")

	for _, field := range []protoreflect.Name{"key", "bucket"} {
		test.True(T, reserves(header, field), test.Sprintf("UploadObjectHeader does not reserve %q", field))
		test.Nil(T, header.Fields().ByName(field))
	}
}

// TestARegistrationCannotClaimASize is the size as the bucket's fact rather
// than the client's.
func TestARegistrationCannotClaimASize(T *testing.T) {
	T.Parallel()

	record := messageNamed(T, "RecordObjectRequest")

	test.True(T, reserves(record, "size"))
	test.Nil(T, record.Fields().ByName("size"))
}

func messageNamed(tb testing.TB, name protoreflect.Name) protoreflect.MessageDescriptor {
	tb.Helper()

	message := mediaregistrypb.File_primandproper_platform_mediaregistry_v1_mediaregistry_proto.Messages().ByName(name)
	must.NotNil(tb, message, must.Sprintf("no message %s in the file", name))

	return message
}

func reserves(message protoreflect.MessageDescriptor, name protoreflect.Name) bool {
	return message.ReservedNames().Has(name)
}
