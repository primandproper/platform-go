package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// TestTheScopeNameIsReservedEverywhere is what makes "the tenant comes off the
// connection" a property of the schema rather than of this package.
//
// A comment saying a request must not name a scope is a request to the next
// author; `reserved "scope"` is a schema protoc refuses the field into, here and
// in a consumer's fork of the file alike. It is audit/grpc's pattern, adopted
// rather than re-derived.
//
// Every message in the file reserves it rather than only the requests. A
// response's rows all belong to the scope the connection resolved, so a scope
// field would tell a client something it supplied — and its absence is what
// makes a converter unable to read one back out of a request.
func TestTheScopeNameIsReservedEverywhere(T *testing.T) {
	T.Parallel()

	messages := issuereportspb.File_primandproper_platform_issuereports_v1_issuereports_proto.Messages()
	must.Positive(T, messages.Len(), must.Sprint("no messages in the file, so this asserted nothing"))

	for i := range messages.Len() {
		message := messages.Get(i)

		// The two empty responses have nothing to say about a scope and nothing
		// to hide one in; a reservation on them would be paperwork.
		if message.Fields().Len() == 0 {
			continue
		}

		T.Run(string(message.Name()), func(t *testing.T) {
			t.Parallel()

			test.True(t, reserves(message, "scope"), test.Sprintf(
				"%s does not reserve the name \"scope\", so protoc would accept one being added", message.Name()))
		})
	}
}

// TestNoWriteCanCarryAReporter is the structural half of "a report is filed by
// whoever is calling".
//
// The suite's CreateReport tests show the reporter comes off the principal; this
// one shows there is nowhere for a request to put a different one. A field added
// to the creation input in a later revision fails here rather than as one
// person's words filed under another's name.
func TestNoWriteCanCarryAReporter(T *testing.T) {
	T.Parallel()

	// The four messages a write is assembled from. ListReportsByReporterRequest
	// is deliberately not among them: it names whose list to page rather than
	// whose report to write, and the row-level rule is what gates it.
	for _, name := range []protoreflect.FullName{
		"primandproper.platform.issuereports.v1.IssueReportCreationInput",
		"primandproper.platform.issuereports.v1.IssueReportUpdateInput",
		"primandproper.platform.issuereports.v1.CreateReportRequest",
		"primandproper.platform.issuereports.v1.UpdateReportRequest",
	} {
		T.Run(string(name), func(t *testing.T) {
			t.Parallel()

			message := messageNamed(t, name)

			test.True(t, reserves(message, "reporter"), test.Sprintf(
				"%s does not reserve the name \"reporter\", so protoc would accept one being added", name))

			test.Nil(t, message.Fields().ByName("reporter"), test.Sprintf(
				"%s carries a reporter field", name))
		})
	}
}

// TestNoWriteCanCarryAStatus is the same argument for the lifecycle: a report is
// born open, and it moves through one door.
//
// A create that could name a status would be a transition spelled as a create —
// the one move that skips the guard the rest of the lifecycle rests on — and a
// revision that could name one would be a whole-row write that silently reopened
// a report somebody had just resolved.
func TestNoWriteCanCarryAStatus(T *testing.T) {
	T.Parallel()

	for _, name := range []protoreflect.FullName{
		"primandproper.platform.issuereports.v1.IssueReportCreationInput",
		"primandproper.platform.issuereports.v1.IssueReportUpdateInput",
	} {
		T.Run(string(name), func(t *testing.T) {
			t.Parallel()

			message := messageNamed(t, name)

			test.True(t, reserves(message, "status"), test.Sprintf(
				"%s does not reserve the name \"status\"", name))
			test.Nil(t, message.Fields().ByName("status"))
		})
	}
}

// TestTheMoveCarriesBothStatuses is the acceptance test for the guard as a
// schema fact.
//
// A transition request that carried only the target status would be a write with
// no compare, and the queue two people can work would become one they cannot —
// silently, since the statement would still succeed for both of them.
func TestTheMoveCarriesBothStatuses(T *testing.T) {
	T.Parallel()

	message := messageNamed(T, "primandproper.platform.issuereports.v1.TransitionReportRequest")

	for _, field := range []protoreflect.Name{"expected_status", "target_status"} {
		descriptor := message.Fields().ByName(field)
		must.NotNil(T, descriptor, must.Sprintf("TransitionReportRequest has no %s", field))

		test.EqOp(T, protoreflect.EnumKind, descriptor.Kind(), test.Sprintf(
			"%s is not the Status enum, so a client could name a status this queue does not have", field))
	}
}

// TestTheStatusEnumCoversTheLifecycle keeps the wire vocabulary exactly as
// complete as the column.
//
// The enum is this module's own closed set, which is the reason it is an enum at
// all — unlike kind and subject_type, which are a consumer's catalog and stay
// strings. A status added to issuereports and not to the .proto would render as
// UNSPECIFIED, which is a real row reaching a client as "we don't know".
func TestTheStatusEnumCoversTheLifecycle(T *testing.T) {
	T.Parallel()

	must.SliceNotEmpty(T, issuereports.Statuses)

	for _, status := range issuereports.Statuses {
		T.Run(status.String(), func(t *testing.T) {
			t.Parallel()

			rendered := issuereportsgrpc.StatusToProto(status)
			test.NotEqOp(t, issuereportspb.Status_STATUS_UNSPECIFIED, rendered, test.Sprintf(
				"%q has no member in the Status enum", status))

			// And back, because a value that renders and does not read is a
			// filter a client cannot ask for.
			test.EqOp(t, status, issuereportsgrpc.StatusFromProto(rendered))
		})
	}

	// The other direction: an enum member this module does not serve would be a
	// queue a client can name and no store can page.
	values := issuereportspb.Status(0).Descriptor().Values()
	test.EqOp(T, len(issuereports.Statuses)+1, values.Len(), test.Sprint(
		"the enum and the lifecycle disagree about how many statuses there are, counting UNSPECIFIED"))
}

// TestTheServiceIsTenMethods pins the count the .proto's service comment argues
// for, so that an eleventh arrives with a failing test naming the argument
// rather than as a diff nobody weighed against it.
func TestTheServiceIsTenMethods(T *testing.T) {
	T.Parallel()

	methods := issuereportspb.File_primandproper_platform_issuereports_v1_issuereports_proto.
		Services().ByName("IssueReportsService").Methods()

	test.EqOp(T, 10, methods.Len())
}

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
