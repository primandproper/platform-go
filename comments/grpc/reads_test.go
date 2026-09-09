package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	commentsgrpc "github.com/primandproper/platform-go/v14/comments/grpc"

	"github.com/primandproper/primitives-go/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

func TestServer_GetComment(T *testing.T) {
	T.Parallel()

	T.Run("reads one of the caller's tenant's comments", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		comment := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "said"})

		res, err := h.server.GetComment(h.ctx(t), &commentspb.GetCommentRequest{CommentId: comment.ID})
		must.NoError(t, err)

		test.EqOp(t, comment.ID, res.GetResult().GetId())
		test.EqOp(t, "said", res.GetResult().GetBody())
		test.EqOp(t, testUser, res.GetResult().GetAuthor())
	})

	// The scope is bound into the statement rather than checked in front of it,
	// so the neighbor's row is absent rather than forbidden.
	T.Run("reads another tenant's comment as absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		comment := h.seed(t, otherScope, &comments.Comment{Target: testTarget})

		_, err := h.server.GetComment(h.ctx(t), &commentspb.GetCommentRequest{CommentId: comment.ID})

		mustBeCode(t, err, codes.NotFound)
		test.ErrorIs(t, err, comments.ErrCommentNotFound)
	})

	T.Run("refuses a request with nobody on it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.GetComment(t.Context(), &commentspb.GetCommentRequest{CommentId: "whatever"})

		mustBeCode(t, err, codes.Unauthenticated)
		test.ErrorIs(t, err, commentsgrpc.ErrNoPrincipal)
	})
}

func TestServer_ListRootComments(T *testing.T) {
	T.Parallel()

	T.Run("pages the top level of one target's discussion", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		root := h.seed(t, testScope, &comments.Comment{Target: testTarget})
		h.seed(t, testScope, &comments.Comment{ParentID: root.ID})

		res, err := h.server.ListRootComments(h.ctx(t), &commentspb.ListRootCommentsRequest{
			Target: &commentspb.CommentTarget{Type: string(recipeType), Id: testTarget.ID},
		})
		must.NoError(t, err)

		// The roots, and only the roots: the reply is a separate question.
		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, root.ID, res.GetResults()[0].GetId())
		test.NotNil(t, res.GetPagination())
	})

	T.Run("returns none of another tenant's", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seed(t, otherScope, &comments.Comment{Target: testTarget})

		res, err := h.server.ListRootComments(h.ctx(t), &commentspb.ListRootCommentsRequest{
			Target: &commentspb.CommentTarget{Type: string(recipeType), Id: testTarget.ID},
		})
		must.NoError(t, err)

		test.SliceEmpty(t, res.GetResults())
	})

	// A filter nothing recognizes is the caller's to fix, so it is the one read
	// failure that is InvalidArgument rather than the mapper's answer.
	T.Run("refuses a filter it cannot read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		sideways := "sideways"

		_, err := h.server.ListRootComments(h.ctx(t), &commentspb.ListRootCommentsRequest{
			Target: &commentspb.CommentTarget{Type: string(recipeType), Id: testTarget.ID},
			Filter: &filteringpb.QueryFilter{SortBy: &sideways},
		})

		mustBeCode(t, err, codes.InvalidArgument)
	})
}

func TestServer_ListReplies(T *testing.T) {
	T.Parallel()

	T.Run("pages one root's replies", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		root := h.seed(t, testScope, &comments.Comment{Target: testTarget})
		reply := h.seed(t, testScope, &comments.Comment{ParentID: root.ID})

		res, err := h.server.ListReplies(h.ctx(t), &commentspb.ListRepliesRequest{
			Target:   &commentspb.CommentTarget{Type: string(recipeType), Id: testTarget.ID},
			ParentId: root.ID,
		})
		must.NoError(t, err)

		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, reply.ID, res.GetResults()[0].GetId())
	})

	// The empty parent is what a root stores, so answering it with the roots
	// would be the wrong half of the discussion with nothing saying so.
	T.Run("refuses a request naming no parent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.ListReplies(h.ctx(t), &commentspb.ListRepliesRequest{
			Target: &commentspb.CommentTarget{Type: string(recipeType), Id: testTarget.ID},
		})

		mustBeCode(t, err, codes.InvalidArgument)
		test.ErrorIs(t, err, comments.ErrEmptyParent)
	})

	// A reply outlives the comment it replies to.
	T.Run("answers with replies whose parent has been archived", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		root := h.seed(t, testScope, &comments.Comment{Target: testTarget})
		reply := h.seed(t, testScope, &comments.Comment{ParentID: root.ID})

		_, err := h.server.ArchiveComment(h.ctx(t), &commentspb.ArchiveCommentRequest{CommentId: root.ID})
		must.NoError(t, err)

		res, err := h.server.ListReplies(h.ctx(t), &commentspb.ListRepliesRequest{
			Target:   &commentspb.CommentTarget{Type: string(recipeType), Id: testTarget.ID},
			ParentId: root.ID,
		})
		must.NoError(t, err)

		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, reply.ID, res.GetResults()[0].GetId())
	})
}

func TestServer_ListCommentsByTargetType(T *testing.T) {
	T.Parallel()

	T.Run("pages roots and replies alike across every target of the type", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		root := h.seed(t, testScope, &comments.Comment{Target: testTarget})
		h.seed(t, testScope, &comments.Comment{ParentID: root.ID})
		h.seed(t, testScope, &comments.Comment{Target: comments.Target{Type: recipeType, ID: "recipe_2"}})
		h.seed(t, testScope, &comments.Comment{Target: comments.Target{Type: mealType, ID: "meal_1"}})

		res, err := h.server.ListCommentsByTargetType(h.ctx(t),
			&commentspb.ListCommentsByTargetTypeRequest{TargetType: string(recipeType)})
		must.NoError(t, err)

		test.SliceLen(t, 3, res.GetResults())
	})

	// The catalog gates writes and not reads: the type an operator has withdrawn
	// is exactly the one whose rows they still need to reach.
	T.Run("answers for a target type the catalog no longer holds", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seed(t, testScope, &comments.Comment{Target: comments.Target{Type: mealType, ID: "meal_1"}})

		// The catalog is narrowed to recipes after the comment was written,
		// which is what withdrawing a target type looks like.
		withdrawn := newHarnessWithTargets(t, comments.Targets{recipeType: {Description: "a recipe"}})
		withdrawn.seed(t, testScope, &comments.Comment{Target: testTarget})

		res, err := h.server.ListCommentsByTargetType(h.ctx(t),
			&commentspb.ListCommentsByTargetTypeRequest{TargetType: string(mealType)})
		must.NoError(t, err)

		test.SliceLen(t, 1, res.GetResults())

		// And on the narrowed catalog the read is still answered rather than
		// refused.
		res, err = withdrawn.server.ListCommentsByTargetType(withdrawn.ctx(t),
			&commentspb.ListCommentsByTargetTypeRequest{TargetType: string(unknownType)})
		must.NoError(t, err)

		test.SliceEmpty(t, res.GetResults())
	})
}

func TestServer_ListCommentsByAuthor(T *testing.T) {
	T.Parallel()

	// An empty author is the caller's own, which is what a "your comments" view
	// sends and the one value that reaches the store unasked.
	T.Run("reads the caller's own comments when the request names nobody", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		mine := h.seed(t, testScope, &comments.Comment{Target: testTarget})
		h.seed(t, testScope, &comments.Comment{Target: testTarget, Author: otherUser})

		res, err := h.server.ListCommentsByAuthor(h.ctx(t), &commentspb.ListCommentsByAuthorRequest{})
		must.NoError(t, err)

		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, mine.ID, res.GetResults()[0].GetId())
	})

	T.Run("reads the caller's own comments when the request names them", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seed(t, testScope, &comments.Comment{Target: testTarget})

		res, err := h.server.ListCommentsByAuthor(h.ctx(t),
			&commentspb.ListCommentsByAuthorRequest{Author: testUser})
		must.NoError(t, err)

		test.SliceLen(t, 1, res.GetResults())
	})

	// Named before any read, so the refusal is PermissionDenied and says nothing
	// about whether that identifier belongs to anybody.
	T.Run("refuses somebody else's comments under the default authorizer", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seed(t, testScope, &comments.Comment{Target: testTarget, Author: otherUser})

		_, err := h.server.ListCommentsByAuthor(h.ctx(t),
			&commentspb.ListCommentsByAuthorRequest{Author: otherUser})

		mustBeCode(t, err, codes.PermissionDenied)
		test.ErrorIs(t, err, commentsgrpc.ErrTargetNotPermitted)
	})

	T.Run("refuses an author nobody has heard of the same way", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.ListCommentsByAuthor(h.ctx(t),
			&commentspb.ListCommentsByAuthorRequest{Author: "user_nobody"})

		mustBeCode(t, err, codes.PermissionDenied)
	})

	T.Run("permits somebody else's comments where the authorizer allows it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, commentsgrpc.WithAuthorAuthorizer(permitAll{}))
		h.seed(t, testScope, &comments.Comment{Target: testTarget, Author: otherUser})

		res, err := h.server.ListCommentsByAuthor(h.ctx(t),
			&commentspb.ListCommentsByAuthorRequest{Author: otherUser})
		must.NoError(t, err)

		test.SliceLen(t, 1, res.GetResults())
	})

	// A caller whose principal reports no identifier resolves an empty request
	// field to an empty author, and the store refuses that rather than reading
	// it as a wildcard — which is the answer that matters, because the
	// short-circuit above would otherwise have let the seam through unasked.
	T.Run("refuses a caller whose principal names nobody", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seed(t, testScope, &comments.Comment{Target: testTarget})

		ctx := withPrincipal(t.Context(), &testPrincipal{scope: testScope})

		_, err := h.server.ListCommentsByAuthor(ctx, &commentspb.ListCommentsByAuthorRequest{})

		mustBeCode(t, err, codes.InvalidArgument)
		test.ErrorIs(t, err, comments.ErrEmptyAuthor)
	})

	T.Run("returns none of another tenant's, even for the same author", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seed(t, otherScope, &comments.Comment{Target: testTarget, Author: testUser})

		res, err := h.server.ListCommentsByAuthor(h.ctx(t), &commentspb.ListCommentsByAuthorRequest{})
		must.NoError(t, err)

		test.SliceEmpty(t, res.GetResults())
	})
}
