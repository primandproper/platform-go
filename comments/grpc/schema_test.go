package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/comments/commentspb"

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
// Every message in the file reserves it, responses included. Every row a
// response returns belongs to the scope the connection resolved, so a scope
// field would tell a client something it supplied — and its absence is what
// makes a converter unable to read one back out of a request.
func TestTheScopeNameIsReserved(T *testing.T) {
	T.Parallel()

	for _, name := range everyMessage(T) {
		T.Run(string(name), func(t *testing.T) {
			t.Parallel()

			test.True(t, reserves(t, name, "scope"), test.Sprintf(
				"%s does not reserve the name \"scope\", so protoc would accept one being added", name))
		})
	}
}

// TestTheAuthorNameIsReservedOnEveryInput is the same mechanism applied to the
// other fact that comes off the connection.
//
// A comment is a sentence attributed to somebody. The two messages a caller
// supplies reserve the name, so authorship cannot become a request field without
// somebody deleting the reservation and arguing for it.
func TestTheAuthorNameIsReservedOnEveryInput(T *testing.T) {
	T.Parallel()

	inputs := []protoreflect.FullName{
		"primandproper.platform.comments.v1.CommentInput",
		"primandproper.platform.comments.v1.UpdateCommentRequest",
	}

	for _, name := range inputs {
		T.Run(string(name), func(t *testing.T) {
			t.Parallel()

			test.True(t, reserves(t, name, "author"), test.Sprintf(
				"%s does not reserve the name \"author\", so a client could name who wrote a comment", name))
		})
	}
}

// TestTheUpdateReservesEverythingItCannotAssign is the schema half of "an edit
// does not move the comment".
//
// The store's write assigns the body alone, so a request carrying a target, a
// parent or an author would describe an edit this service cannot perform — and
// the honest way to say that is a schema in which those fields cannot exist.
func TestTheUpdateReservesEverythingItCannotAssign(T *testing.T) {
	T.Parallel()

	const name protoreflect.FullName = "primandproper.platform.comments.v1.UpdateCommentRequest"

	for _, field := range []string{"target", "parent_id", "author"} {
		T.Run(field, func(t *testing.T) {
			t.Parallel()

			test.True(t, reserves(t, name, protoreflect.Name(field)), test.Sprintf(
				"UpdateCommentRequest does not reserve %q, which the write cannot assign", field))
		})
	}
}

// TestTheTargetTypeIsAStringEverywhere is the opaque-catalog ruling, checked
// rather than described.
//
// Which kinds of thing accept comments is the consumer's catalog. A generated
// enum would put their vocabulary on this module's release cadence, so every
// field carrying one is a string and a later revision that made it an enum fails
// here.
func TestTheTargetTypeIsAStringEverywhere(T *testing.T) {
	T.Parallel()

	messages := commentspb.File_primandproper_platform_comments_v1_comments_proto.Messages()
	must.Positive(T, messages.Len(), must.Sprint("no messages in the file, so this asserted nothing"))

	for i := range messages.Len() {
		message := messages.Get(i)

		fields := message.Fields()
		for j := range fields.Len() {
			field := fields.Get(j)

			if field.Name() != "type" && field.Name() != "target_type" {
				continue
			}

			test.EqOp(T, protoreflect.StringKind, field.Kind(), test.Sprintf(
				"%s.%s is not a string; the target catalog is the consumer's", message.Name(), field.Name()))
		}
	}
}

// TestNoMessageCarriesAnEnum is the wider form of the ruling above: this schema
// defines no enum at all.
//
// Nothing in this package's vocabulary is a closed set it owns. settings.Kind is
// the opposite case and is an enum there; here every named thing — a target
// type, an author, a body — belongs to the application.
func TestNoMessageCarriesAnEnum(T *testing.T) {
	T.Parallel()

	file := commentspb.File_primandproper_platform_comments_v1_comments_proto

	test.EqOp(T, 0, file.Enums().Len(), test.Sprint(
		"comments.proto declares an enum; every value in it is the consumer's vocabulary"))
}

// TestTheServiceIsEightMethods pins the count the .proto's service comment
// argues for, so that a ninth arrives with a failing test naming the argument
// rather than as a diff nobody weighed against it.
func TestTheServiceIsEightMethods(T *testing.T) {
	T.Parallel()

	methods := commentspb.File_primandproper_platform_comments_v1_comments_proto.
		Services().ByName("CommentsService").Methods()

	test.EqOp(T, 8, methods.Len())
}

// everyMessage is every message the file declares, so the reservation tests
// cover a message added later rather than the ones somebody remembered.
func everyMessage(tb testing.TB) []protoreflect.FullName {
	tb.Helper()

	messages := commentspb.File_primandproper_platform_comments_v1_comments_proto.Messages()
	must.Positive(tb, messages.Len(), must.Sprint("no messages in the file, so this asserted nothing"))

	out := make([]protoreflect.FullName, 0, messages.Len())

	for i := range messages.Len() {
		out = append(out, messages.Get(i).FullName())
	}

	return out
}

// reserves reports whether the named message reserves the given field name.
func reserves(tb testing.TB, name protoreflect.FullName, field protoreflect.Name) bool {
	tb.Helper()

	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
	must.NoError(tb, err)

	message, ok := descriptor.(protoreflect.MessageDescriptor)
	must.True(tb, ok)

	reserved := message.ReservedNames()
	for i := range reserved.Len() {
		if reserved.Get(i) == field {
			return true
		}
	}

	return false
}
