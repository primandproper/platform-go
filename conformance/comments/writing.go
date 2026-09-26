package comments

import (
	"testing"

	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// The bodies most of these assertions write. Their wording is irrelevant; what
// matters is that a read-back can tell them apart.
const (
	bodyRoot  = "the sauce needs salt"
	bodyReply = "agreed, and less sugar"
)

func writing(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a comment is answered as it was stored and reads back the same", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		about := target(t, s)

		stored := say(t, caller, about, bodyRoot)

		test.NotEq(t, "", stored.GetId(), test.Sprint("the store minted no identifier"))
		test.EqOp(t, bodyRoot, stored.GetBody())
		test.EqOp(t, about.GetType(), stored.GetTarget().GetType())
		test.EqOp(t, about.GetId(), stored.GetTarget().GetId())
		test.EqOp(t, "", stored.GetParentId(), test.Sprint("a comment naming a target came back as a reply"))
		test.True(t, stored.GetCreatedAt().IsValid(), test.Sprint("the comment came back with no creation time"))
		test.Nil(t, stored.GetLastUpdatedAt(), test.Sprint("a comment nobody edited came back marked as edited"))

		got := read(t, caller, stored.GetId())
		test.EqOp(t, stored.GetId(), got.GetId())
		test.EqOp(t, bodyRoot, got.GetBody())
		test.EqOp(t, about.GetId(), got.GetTarget().GetId())
	})

	// The whole reason author is not a request field: a caller cannot say who
	// wrote a comment, because the schema has nowhere for them to say it and the
	// handler reads the principal instead.
	t.Run("a comment is attributed to the caller who wrote it", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsUser(t, caller)

		stored := say(t, caller, target(t, s), bodyRoot)
		test.EqOp(t, caller.UserID, stored.GetAuthor())
		test.EqOp(t, caller.UserID, read(t, caller, stored.GetId()).GetAuthor())
	})

	t.Run("a reply adopts its parent's target", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		about := target(t, s)
		root := say(t, caller, about, bodyRoot)

		answer := reply(t, caller, root.GetId(), bodyReply)

		test.EqOp(t, root.GetId(), answer.GetParentId())
		test.EqOp(t, about.GetType(), answer.GetTarget().GetType())
		test.EqOp(t, about.GetId(), answer.GetTarget().GetId())
	})

	// The six refusals below this one are each a thing a person typed and can
	// fix, and four of them share a code — so the message is the only part of
	// the answer that says which field to put a red border around, and it is
	// asserted to arrive.
	t.Run("a reply to a reply is refused, and the client is told why", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		root := say(t, caller, target(t, s), bodyRoot)
		answer := reply(t, caller, root.GetId(), bodyReply)

		_, err := caller.Surfaces.Comments.CreateComment(caller.Context(t.Context()), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{ParentId: answer.GetId(), Body: "and another thing"},
		})
		refused(t, err, codes.InvalidArgument, "a reply to a reply")
		saying(t, err, "may not itself be replied to")
	})

	t.Run("a target type the deployment takes no comments on is refused, and the client is told why", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		target(t, s) // The skip, where the subject names no target type at all.

		_, err := caller.Surfaces.Comments.CreateComment(caller.Context(t.Context()), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{
				Target: &commentspb.CommentTarget{Type: "conformance_unaccepted_" + identifiers.New(), Id: "x"},
				Body:   "about something this application does not have",
			},
		})
		refused(t, err, codes.InvalidArgument, "a comment on an undeclared target type")
		saying(t, err, "target type")
	})

	t.Run("a reply naming a different target than its parent is refused, and the client is told why", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		root := say(t, caller, target(t, s), bodyRoot)

		// Another thing of the same type, which is the mismatch a client can
		// actually make: a reply form that posted the page it was rendered on
		// rather than the discussion it was opened from.
		_, err := caller.Surfaces.Comments.CreateComment(caller.Context(t.Context()), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{ParentId: root.GetId(), Target: target(t, s), Body: bodyReply},
		})
		refused(t, err, codes.InvalidArgument, "a reply filed under another target")
		saying(t, err, "different target")
	})

	t.Run("an empty comment is refused, and the client is told why", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := caller.Surfaces.Comments.CreateComment(caller.Context(t.Context()), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{Target: target(t, s)},
		})
		refused(t, err, codes.InvalidArgument, "an empty comment")
		saying(t, err, "empty comment body")
	})

	t.Run("a request carrying no comment at all is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := caller.Surfaces.Comments.CreateComment(caller.Context(t.Context()), &commentspb.CreateCommentRequest{})
		refused(t, err, codes.InvalidArgument, "a request with no comment in it")
	})

	t.Run("an edit revises the caller's own comment and marks it edited", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		stored := say(t, caller, target(t, s), "frist")

		edited, err := caller.Surfaces.Comments.UpdateComment(caller.Context(t.Context()),
			&commentspb.UpdateCommentRequest{CommentId: stored.GetId(), Body: "first"})
		must.NoError(t, err)

		test.EqOp(t, "first", edited.GetResult().GetBody())

		// The mark is on the edit's own answer, which is what lets a client
		// render "edited" without a second call.
		must.NotNil(t, edited.GetResult().GetLastUpdatedAt(), must.Sprint("an edit came back unmarked"))
		test.True(t, edited.GetResult().GetLastUpdatedAt().IsValid())

		got := read(t, caller, stored.GetId())
		test.EqOp(t, "first", got.GetBody())
		test.NotNil(t, got.GetLastUpdatedAt(), test.Sprint("an edited comment reads back unmarked"))
	})

	t.Run("an edit emptying a comment is refused and changes nothing", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		stored := say(t, caller, target(t, s), bodyRoot)

		_, err := caller.Surfaces.Comments.UpdateComment(caller.Context(t.Context()),
			&commentspb.UpdateCommentRequest{CommentId: stored.GetId()})
		refused(t, err, codes.InvalidArgument, "an edit to nothing")
		saying(t, err, "empty comment body")

		got := read(t, caller, stored.GetId())
		test.EqOp(t, bodyRoot, got.GetBody())
		test.Nil(t, got.GetLastUpdatedAt(), test.Sprint("a refused edit marked the comment edited"))
	})

	t.Run("an archived comment is gone from its discussion", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		about := target(t, s)
		stored := say(t, caller, about, bodyRoot)

		// The positive control: the comment is in the discussion before it is
		// archived, so its absence afterwards is the archive's doing.
		test.SliceContains(t, commentIDs(roots(t, caller, about).GetResults()), stored.GetId())

		_, err := caller.Surfaces.Comments.ArchiveComment(caller.Context(t.Context()),
			&commentspb.ArchiveCommentRequest{CommentId: stored.GetId()})
		must.NoError(t, err)

		_, err = caller.Surfaces.Comments.GetComment(caller.Context(t.Context()),
			&commentspb.GetCommentRequest{CommentId: stored.GetId()})
		refused(t, err, codes.NotFound, "reading an archived comment")

		test.SliceNotContains(t, commentIDs(roots(t, caller, about).GetResults()), stored.GetId(),
			test.Sprint("an archived comment is still in its discussion"))
	})

	// The discussion an administrator asks for with the archive in it. An
	// administrator holds the grant that archives a comment, and that is the
	// grant a deployment reads the archive off — so this half, unlike an
	// ordinary caller's, has one answer everywhere.
	t.Run("an administrator asking for archived comments receives them", func(t *testing.T) {
		t.Parallel()

		admin := s.Subject(t, conformance.AsAdmin())
		about := target(t, s)
		live := say(t, admin, about, bodyRoot)
		removed := say(t, admin, about, bodyReply)

		ctx := admin.Context(t.Context())

		_, err := admin.Surfaces.Comments.ArchiveComment(ctx,
			&commentspb.ArchiveCommentRequest{CommentId: removed.GetId()})
		must.NoError(t, err)

		// The control: without asking, the archived comment is gone.
		test.SliceNotContains(t, commentIDs(roots(t, admin, about).GetResults()), removed.GetId())

		include := true

		page, err := admin.Surfaces.Comments.ListRootComments(ctx, &commentspb.ListRootCommentsRequest{
			Target: about,
			Filter: &filteringpb.QueryFilter{IncludeArchived: &include},
		})
		must.NoError(t, err)

		ids := commentIDs(page.GetResults())
		test.SliceContains(t, ids, live.GetId())
		test.SliceContains(t, ids, removed.GetId(),
			test.Sprint("an administrator asked for archived comments and was answered without them"))
	})

	t.Run("archiving a comment already archived is answered as absent", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		stored := say(t, caller, target(t, s), bodyRoot)

		_, err := caller.Surfaces.Comments.ArchiveComment(caller.Context(t.Context()),
			&commentspb.ArchiveCommentRequest{CommentId: stored.GetId()})
		must.NoError(t, err)

		_, err = caller.Surfaces.Comments.ArchiveComment(caller.Context(t.Context()),
			&commentspb.ArchiveCommentRequest{CommentId: stored.GetId()})
		refused(t, err, codes.NotFound, "archiving a comment twice")
	})

	// A reply outlives the comment it replies to: taking a root out of the
	// discussion does not take everybody's answers to it along.
	t.Run("archiving a root leaves its replies readable", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		about := target(t, s)
		root := say(t, caller, about, bodyRoot)
		answer := reply(t, caller, root.GetId(), bodyReply)

		_, err := caller.Surfaces.Comments.ArchiveComment(caller.Context(t.Context()),
			&commentspb.ArchiveCommentRequest{CommentId: root.GetId()})
		must.NoError(t, err)

		test.EqOp(t, answer.GetId(), read(t, caller, answer.GetId()).GetId())
		test.SliceContains(t, replies(t, caller, about, root.GetId()), answer.GetId(),
			test.Sprint("archiving a root took its replies with it"))
	})
}
