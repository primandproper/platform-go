package grpc_test

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/audit/auditpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// TestRequestsCannotNameAScope is the acceptance test for the narrowing this
// surface exists to make: a request cannot produce a cross-scope read, whatever
// it sends.
//
// The server-side half is asserted in server_test.go, against a real chain in
// two tenants. This is the half no request can reach: the schema has no field
// for a scope, in any message, so there is nothing for a handler to read one
// from and nothing for a converter to be asked to honor. audit.Query.Scope is a
// *string in which nil means every tenant's events, and a field here is the
// exact shape that would put that choice in a client's hands.
//
// It reads the file descriptor rather than the generated structs, so it is a
// statement about the schema every language generates from and not about Go.
func TestRequestsCannotNameAScope(T *testing.T) {
	T.Parallel()

	file := auditpb.File_primandproper_platform_audit_v1_audit_proto
	must.NotNil(T, file)

	messages := file.Messages()
	must.Positive(T, messages.Len())

	for i := range messages.Len() {
		message := messages.Get(i)

		T.Run(string(message.Name()), func(t *testing.T) {
			t.Parallel()

			fields := message.Fields()

			for j := range fields.Len() {
				name := string(fields.Get(j).Name())

				test.False(t, strings.Contains(name, "scope"), test.Sprintf(
					"%s.%s names a scope, which is the field this service exists not to have",
					message.Name(), name))
			}
		})
	}
}

// TestTheScopeNameIsReserved is the other half, and the one that makes the
// property protoc's rather than this test's.
//
// Every message a request is built from reserves the name, so adding
// `string scope = N;` to one is a schema that does not compile — here, and in a
// consumer's fork of the file. A test can only catch what somebody ran it
// against.
func TestTheScopeNameIsReserved(T *testing.T) {
	T.Parallel()

	requests := []protoreflect.FullName{
		"primandproper.platform.audit.v1.GetEntryRequest",
		"primandproper.platform.audit.v1.ListEntriesRequest",
		"primandproper.platform.audit.v1.VerifyChainRequest",
		"primandproper.platform.audit.v1.EntryQuery",
		"primandproper.platform.audit.v1.Entry",
		"primandproper.platform.audit.v1.VerificationResult",
	}

	for _, name := range requests {
		T.Run(string(name), func(t *testing.T) {
			t.Parallel()

			descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
			must.NoError(t, err)

			message, ok := descriptor.(protoreflect.MessageDescriptor)
			must.True(t, ok)

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

// TestTheServiceIsThreeReads pins the other narrowing: there is no recording
// RPC, and audit.Recorder says why one cannot exist.
func TestTheServiceIsThreeReads(T *testing.T) {
	T.Parallel()

	methods := auditpb.File_primandproper_platform_audit_v1_audit_proto.
		Services().ByName("AuditService").Methods()

	names := make([]string, 0, methods.Len())
	for i := range methods.Len() {
		names = append(names, string(methods.Get(i).Name()))
	}

	test.SliceContainsAll(T, []string{"GetEntry", "ListEntries", "VerifyChain"}, names)

	for _, name := range names {
		test.False(T, strings.Contains(strings.ToLower(name), "record"), test.Sprintf(
			"%s writes to the log, which is a call that belongs inside the caller's transaction", name))
	}
}
