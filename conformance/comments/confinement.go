package comments

import (
	"testing"

	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

func confinement(t *testing.T, s *conformance.Session) {
	t.Helper()

	// Asked with a real identifier from the wrong tenant. The scope is bound
	// into every statement rather than checked in front of it, so a neighbor's
	// comment is not refused, it is absent — the answer that is also not an
	// oracle for which identifiers exist.
	t.Run("a neighbor's comment is absent to every call that names it", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		stored := say(t, mine, target(t, s, mine), bodyRoot)

		// The positive control. Every absence below is also what a surface
		// reaching nothing at all would answer.
		test.EqOp(t, stored.GetId(), read(t, mine, stored.GetId()).GetId())

		ctx := theirs.Context(t.Context())

		_, err := theirs.Surfaces.Comments.GetComment(ctx, &commentspb.GetCommentRequest{CommentId: stored.GetId()})
		refused(t, err, codes.NotFound, "reading a neighbor's comment")

		_, err = theirs.Surfaces.Comments.UpdateComment(ctx,
			&commentspb.UpdateCommentRequest{CommentId: stored.GetId(), Body: "rewritten from next door"})
		refused(t, err, codes.NotFound, "editing a neighbor's comment")

		_, err = theirs.Surfaces.Comments.ArchiveComment(ctx, &commentspb.ArchiveCommentRequest{CommentId: stored.GetId()})
		refused(t, err, codes.NotFound, "archiving a neighbor's comment")

		// A reply names its parent by identifier, which makes it a fourth way
		// to reach a row; the parent is absent here for the same reason.
		_, err = theirs.Surfaces.Comments.CreateComment(ctx, &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{ParentId: stored.GetId(), Body: "reaching next door"},
		})
		refused(t, err, codes.NotFound, "replying to a neighbor's comment")

		// And none of it touched the comment.
		got := read(t, mine, stored.GetId())
		test.EqOp(t, bodyRoot, got.GetBody())
		test.Nil(t, got.GetLastUpdatedAt(), test.Sprint("a neighbor's refused edit marked the comment edited"))
		test.SliceContains(t, commentIDs(roots(t, mine, got.GetTarget()).GetResults()), stored.GetId(),
			test.Sprint("a neighbor's refused archive took the comment out of its discussion"))
	})

	// Both tenants talk about the same target where the subject's target type
	// takes any identifier — the same type and the same identifier — because a
	// target is the application's thing rather than either tenant's, and
	// "recipe 42" can exist in both. The discussion is still two discussions.
	//
	// Where the subject brings targets into being, each tenant's is made in
	// that tenant, since a thing made in one need not exist in the other. The
	// listings are then asked after the neighbor's target by name, which is
	// the same question from the side a caller can reach.
	t.Run("a discussion's listings reach the caller's tenant only", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		myTarget, theirTarget := eachTarget(t, s, mine, theirs)

		myRoot := say(t, mine, myTarget, bodyRoot)
		myReply := reply(t, mine, myRoot.GetId(), bodyReply)
		theirRoot := say(t, theirs, theirTarget, bodyRoot)
		theirReply := reply(t, theirs, theirRoot.GetId(), bodyReply)

		// Presence and absence of named comments, never a count: these
		// listings may run against a database the suite does not own.
		test.SliceContains(t, commentIDs(roots(t, mine, myTarget).GetResults()), myRoot.GetId(),
			test.Sprint("this tenant's own root was missing from its discussion"))
		test.SliceNotContains(t, commentIDs(roots(t, mine, theirTarget).GetResults()), theirRoot.GetId(),
			test.Sprint("a neighboring tenant's root reached this discussion"))

		// The mirror image, which rules out a rule favoring whichever caller
		// was made first.
		test.SliceContains(t, commentIDs(roots(t, theirs, theirTarget).GetResults()), theirRoot.GetId())
		test.SliceNotContains(t, commentIDs(roots(t, theirs, myTarget).GetResults()), myRoot.GetId())

		// The replies, asked for by naming the neighbor's root outright.
		test.SliceContains(t, replies(t, mine, myTarget, myRoot.GetId()), myReply.GetId())
		test.SliceNotContains(t, replies(t, mine, theirTarget, theirRoot.GetId()), theirReply.GetId(),
			test.Sprint("a neighboring tenant's reply was listed by naming its root"))

		// "Your comments" is the caller's in the caller's tenant.
		own, err := byAuthor(t, mine, "")
		must.NoError(t, err)
		test.SliceContains(t, own, myRoot.GetId())
		test.SliceNotContains(t, own, theirRoot.GetId())

		// Last, because it is the one read a deployment may withhold from an
		// ordinary caller, and its skip would skip everything after it.
		got := byTargetType(t, mine, myTarget.GetType())
		test.SliceContains(t, got, myReply.GetId(), test.Sprint("this tenant's own reply was missing from the moderation read"))
		test.SliceNotContains(t, got, theirRoot.GetId(), test.Sprint("a neighboring tenant's root reached the moderation read"))
		test.SliceNotContains(t, got, theirReply.GetId(), test.Sprint("a neighboring tenant's reply reached the moderation read"))
	})
}
