package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	commentsgrpc "github.com/primandproper/platform-go/v14/comments/grpc"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestCommentToProto(T *testing.T) {
	T.Parallel()

	T.Run("renders every field the message has", func(t *testing.T) {
		t.Parallel()

		edited := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
		archived := edited.Add(time.Hour)

		out := commentsgrpc.CommentToProto(&comments.Comment{
			CreatedAt:     edited.Add(-time.Hour),
			LastUpdatedAt: &edited,
			ArchivedAt:    &archived,
			ID:            "comment_1",
			ParentID:      "comment_0",
			Author:        testUser,
			Body:          "said",
			Target:        testTarget,
			Scope:         testScope,
		})
		must.NotNil(t, out)

		test.EqOp(t, "comment_1", out.GetId())
		test.EqOp(t, "comment_0", out.GetParentId())
		test.EqOp(t, testUser, out.GetAuthor())
		test.EqOp(t, "said", out.GetBody())
		test.EqOp(t, string(recipeType), out.GetTarget().GetType())
		test.EqOp(t, testTarget.ID, out.GetTarget().GetId())
		test.EqOp(t, edited.UTC(), out.GetLastUpdatedAt().AsTime())
		test.EqOp(t, archived.UTC(), out.GetArchivedAt().AsTime())
	})

	// The scope is on the entity and has nowhere to go, which is the property
	// the schema's reservation exists to keep. Encoding the message is the
	// broadest reading of "not readable through the surface": asserting on the
	// fields a converter happens to set would pass a message that grew a new
	// one.
	T.Run("carries no scope onto the wire", func(t *testing.T) {
		t.Parallel()

		out := commentsgrpc.CommentToProto(&comments.Comment{
			ID:     "comment_1",
			Body:   "said",
			Scope:  tenancy.Of("acct_secret"),
			Target: testTarget,
		})

		encoded, err := proto.Marshal(out)
		must.NoError(t, err)

		test.StrNotContains(t, string(encoded), "acct_secret")
	})

	// The two nullable times stay unset rather than becoming 1970, which is what
	// a client renders an "edited" marker from.
	T.Run("leaves an unrevised comment's times unset", func(t *testing.T) {
		t.Parallel()

		out := commentsgrpc.CommentToProto(&comments.Comment{ID: "comment_1"})

		test.Nil(t, out.GetLastUpdatedAt())
		test.Nil(t, out.GetArchivedAt())
	})

	T.Run("renders a nil comment as nil", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, commentsgrpc.CommentToProto(nil))
	})
}

func TestCommentsToProto(T *testing.T) {
	T.Parallel()

	T.Run("renders a page", func(t *testing.T) {
		t.Parallel()

		out := commentsgrpc.CommentsToProto([]*comments.Comment{{ID: "a"}, {ID: "b"}})

		must.SliceLen(t, 2, out)
		test.EqOp(t, "a", out[0].GetId())
		test.EqOp(t, "b", out[1].GetId())
	})

	// An empty page is an empty slice rather than nil, so a client reading a
	// count off it does not have to branch.
	T.Run("renders an empty page as an empty slice", func(t *testing.T) {
		t.Parallel()

		out := commentsgrpc.CommentsToProto(nil)

		must.NotNil(t, out)
		test.SliceEmpty(t, out)
	})
}

func TestTargetConversion(T *testing.T) {
	T.Parallel()

	T.Run("round-trips a target", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, testTarget, commentsgrpc.TargetFromProto(commentsgrpc.TargetToProto(testTarget)))
	})

	// The zero target is what a reply adopting its parent's looks like on the
	// way in, so it is a value rather than a failure.
	T.Run("reads a nil message as the zero target", func(t *testing.T) {
		t.Parallel()

		test.True(t, commentsgrpc.TargetFromProto(nil).Zero())
	})

	T.Run("renders the zero target as an empty message", func(t *testing.T) {
		t.Parallel()

		out := commentsgrpc.TargetToProto(comments.Target{})

		must.NotNil(t, out)
		test.EqOp(t, "", out.GetType())
		test.EqOp(t, "", out.GetId())
	})
}

// TestTheInputMessageCannotNameAnAuthorOrAScope is the converter half of the two
// facts that come off the connection. The schema reserves both names; this is
// what a caller sending a message built by hand still cannot do.
func TestTheInputMessageCannotNameAnAuthorOrAScope(T *testing.T) {
	T.Parallel()

	fields := (&commentspb.CommentInput{}).ProtoReflect().Descriptor().Fields()

	for _, name := range []string{"author", "scope", "id"} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.Nil(t, fields.ByName(protoreflect.Name(name)),
				test.Sprintf("CommentInput has a %q field, which the handler would have to ignore", name))
		})
	}
}
