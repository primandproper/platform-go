package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	commentsgrpc "github.com/primandproper/platform-go/v14/comments/grpc"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestServer_CreateComment(T *testing.T) {
	T.Parallel()

	T.Run("writes the comment and answers with what was stored", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.CreateComment(h.ctx(t), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{
				Target: &commentspb.CommentTarget{Type: string(recipeType), Id: testTarget.ID},
				Body:   "the sauce needs salt",
			},
		})
		must.NoError(t, err)
		must.NotNil(t, res.GetResult())

		test.NotEqOp(t, "", res.GetResult().GetId())
		test.EqOp(t, "the sauce needs salt", res.GetResult().GetBody())
		test.EqOp(t, string(recipeType), res.GetResult().GetTarget().GetType())
		test.True(t, res.GetResult().GetCreatedAt().IsValid())
		test.Nil(t, res.GetResult().GetLastUpdatedAt())
	})

	// The whole reason author is not a request field: a caller cannot say who
	// wrote the comment, because the schema has nowhere for them to say it and
	// the handler reads the principal instead.
	T.Run("attributes the comment to the caller and not to the request", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.CreateComment(h.ctxAs(t, otherUser), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{
				Target: &commentspb.CommentTarget{Type: string(recipeType), Id: testTarget.ID},
				Body:   "written by whoever authenticated",
			},
		})
		must.NoError(t, err)

		test.EqOp(t, otherUser, res.GetResult().GetAuthor())
	})

	T.Run("adopts the parent's target on a reply", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		root := h.seed(t, testScope, &comments.Comment{Target: testTarget})

		res, err := h.server.CreateComment(h.ctx(t), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{ParentId: root.ID, Body: "agreed"},
		})
		must.NoError(t, err)

		test.EqOp(t, root.ID, res.GetResult().GetParentId())
		test.EqOp(t, testTarget.ID, res.GetResult().GetTarget().GetId())
	})

	// The six client-safe refusals, each arriving as the sentinel a client can
	// match rather than as a code four of them share.
	T.Run("refuses a reply to a reply", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		root := h.seed(t, testScope, &comments.Comment{Target: testTarget})
		reply := h.seed(t, testScope, &comments.Comment{ParentID: root.ID})

		_, err := h.server.CreateComment(h.ctx(t), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{ParentId: reply.ID, Body: "and another thing"},
		})

		mustBeCode(t, err, codes.InvalidArgument)
		test.ErrorIs(t, err, comments.ErrNestedReply)
	})

	T.Run("refuses a target type outside the catalog", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.CreateComment(h.ctx(t), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{
				Target: &commentspb.CommentTarget{Type: string(unknownType), Id: "x"},
				Body:   "about something this application does not have",
			},
		})

		mustBeCode(t, err, codes.InvalidArgument)
		test.ErrorIs(t, err, comments.ErrUnknownTargetType)
	})

	T.Run("refuses a target a registered check cannot find", func(t *testing.T) {
		t.Parallel()

		// The existence check stays on this side of the surface: it is a Go func
		// the consumer registered, it reads on their own connection, and what
		// crosses the wire is the refusal.
		h := newHarnessWithTargets(t, comments.Targets{
			recipeType: {
				Description: "a recipe",
				Exists: func(context.Context, tenancy.Scope, string) (bool, error) {
					return false, nil
				},
			},
		})

		_, err := h.server.CreateComment(h.ctx(t), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{
				Target: &commentspb.CommentTarget{Type: string(recipeType), Id: "gone"},
				Body:   "about a recipe that was deleted",
			},
		})

		mustBeCode(t, err, codes.NotFound)
		test.ErrorIs(t, err, comments.ErrTargetNotFound)
	})

	T.Run("refuses a reply naming a different target than its parent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		root := h.seed(t, testScope, &comments.Comment{Target: testTarget})

		_, err := h.server.CreateComment(h.ctx(t), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{
				ParentId: root.ID,
				Target:   &commentspb.CommentTarget{Type: string(mealType), Id: "meal_1"},
				Body:     "filed under something else",
			},
		})

		mustBeCode(t, err, codes.InvalidArgument)
		test.ErrorIs(t, err, comments.ErrTargetMismatch)
	})

	T.Run("refuses an empty body", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.CreateComment(h.ctx(t), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{
				Target: &commentspb.CommentTarget{Type: string(recipeType), Id: testTarget.ID},
			},
		})

		mustBeCode(t, err, codes.InvalidArgument)
		test.ErrorIs(t, err, comments.ErrEmptyBody)
	})

	T.Run("refuses a parent in another tenant's scope", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		root := h.seed(t, otherScope, &comments.Comment{Target: testTarget})

		_, err := h.server.CreateComment(h.ctx(t), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{ParentId: root.ID, Body: "reaching next door"},
		})

		mustBeCode(t, err, codes.NotFound)
		test.ErrorIs(t, err, comments.ErrParentNotFound)
	})

	T.Run("refuses a request naming no comment", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.CreateComment(h.ctx(t), &commentspb.CreateCommentRequest{})

		mustBeCode(t, err, codes.InvalidArgument)
		test.ErrorIs(t, err, commentsgrpc.ErrNilCommentInput)
	})

	T.Run("refuses a request with nobody on it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.CreateComment(t.Context(), &commentspb.CreateCommentRequest{
			Comment: &commentspb.CommentInput{
				Target: &commentspb.CommentTarget{Type: string(recipeType), Id: testTarget.ID},
				Body:   "anonymous",
			},
		})

		mustBeCode(t, err, codes.Unauthenticated)
		test.ErrorIs(t, err, commentsgrpc.ErrNoPrincipal)
	})
}

func TestServer_UpdateComment(T *testing.T) {
	T.Parallel()

	T.Run("revises the caller's own comment and stamps it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		comment := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "frist"})

		res, err := h.server.UpdateComment(h.ctx(t), &commentspb.UpdateCommentRequest{
			CommentId: comment.ID,
			Body:      "first",
		})
		must.NoError(t, err)

		test.EqOp(t, "first", res.GetResult().GetBody())

		// The read-back is inside the same transaction as the write, which is
		// what makes an "edited" marker renderable from the response rather than
		// from a second call.
		must.NotNil(t, res.GetResult().GetLastUpdatedAt())
		test.True(t, res.GetResult().GetLastUpdatedAt().IsValid())
	})

	// The second question a grant on the method cannot answer.
	T.Run("refuses somebody else's comment under the default authorizer", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		comment := h.seed(t, testScope, &comments.Comment{Target: testTarget, Author: otherUser})

		_, err := h.server.UpdateComment(h.ctx(t), &commentspb.UpdateCommentRequest{
			CommentId: comment.ID,
			Body:      "words I did not write",
		})

		// NotFound rather than PermissionDenied: the read has already happened,
		// so a distinct code would tell a caller walking identifiers which of
		// them are real.
		mustBeCode(t, err, codes.NotFound)
		test.ErrorIs(t, err, commentsgrpc.ErrTargetNotPermitted)
	})

	T.Run("permits somebody else's comment where the authorizer allows it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, commentsgrpc.WithAuthorAuthorizer(permitAll{}))
		comment := h.seed(t, testScope, &comments.Comment{Target: testTarget, Author: otherUser})

		res, err := h.server.UpdateComment(h.ctx(t), &commentspb.UpdateCommentRequest{
			CommentId: comment.ID,
			Body:      "moderated",
		})
		must.NoError(t, err)

		test.EqOp(t, "moderated", res.GetResult().GetBody())

		// The edit assigns the body and nothing else: the author stays whoever
		// said it.
		test.EqOp(t, otherUser, res.GetResult().GetAuthor())
	})

	// An authorizer that could not decide is not an authorizer that refused.
	T.Run("answers Internal for an authorizer that failed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, commentsgrpc.WithAuthorAuthorizer(brokenAuthorizer{}))
		comment := h.seed(t, testScope, &comments.Comment{Target: testTarget, Author: otherUser})

		_, err := h.server.UpdateComment(h.ctx(t), &commentspb.UpdateCommentRequest{
			CommentId: comment.ID,
			Body:      "moderated",
		})

		mustBeCode(t, err, codes.Internal)
		test.ErrorIs(t, err, errAuthorizerUnavailable)
	})

	T.Run("refuses a comment in another tenant's scope as absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		comment := h.seed(t, otherScope, &comments.Comment{Target: testTarget})

		_, err := h.server.UpdateComment(h.ctx(t), &commentspb.UpdateCommentRequest{
			CommentId: comment.ID,
			Body:      "reaching next door",
		})

		mustBeCode(t, err, codes.NotFound)
		test.ErrorIs(t, err, comments.ErrCommentNotFound)
	})

	T.Run("refuses an empty body", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		comment := h.seed(t, testScope, &comments.Comment{Target: testTarget})

		_, err := h.server.UpdateComment(h.ctx(t), &commentspb.UpdateCommentRequest{CommentId: comment.ID})

		mustBeCode(t, err, codes.InvalidArgument)
		test.ErrorIs(t, err, comments.ErrEmptyBody)
	})

	// The transaction is the point: a refused edit leaves the row alone.
	T.Run("leaves the comment untouched when it refuses", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		comment := h.seed(t, testScope, &comments.Comment{Target: testTarget, Author: otherUser, Body: "as written"})

		_, err := h.server.UpdateComment(h.ctx(t), &commentspb.UpdateCommentRequest{
			CommentId: comment.ID,
			Body:      "as rewritten",
		})
		must.Error(t, err)

		read, err := h.server.GetComment(h.ctx(t), &commentspb.GetCommentRequest{CommentId: comment.ID})
		must.NoError(t, err)

		test.EqOp(t, "as written", read.GetResult().GetBody())
		test.Nil(t, read.GetResult().GetLastUpdatedAt())
	})
}

func TestServer_ArchiveComment(T *testing.T) {
	T.Parallel()

	T.Run("takes the caller's own comment out of the discussion", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		comment := h.seed(t, testScope, &comments.Comment{Target: testTarget})

		_, err := h.server.ArchiveComment(h.ctx(t), &commentspb.ArchiveCommentRequest{CommentId: comment.ID})
		must.NoError(t, err)

		_, err = h.server.GetComment(h.ctx(t), &commentspb.GetCommentRequest{CommentId: comment.ID})
		mustBeCode(t, err, codes.NotFound)
	})

	T.Run("leaves a root's replies where they are", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		root := h.seed(t, testScope, &comments.Comment{Target: testTarget})
		reply := h.seed(t, testScope, &comments.Comment{ParentID: root.ID})

		_, err := h.server.ArchiveComment(h.ctx(t), &commentspb.ArchiveCommentRequest{CommentId: root.ID})
		must.NoError(t, err)

		read, err := h.server.GetComment(h.ctx(t), &commentspb.GetCommentRequest{CommentId: reply.ID})
		must.NoError(t, err)

		test.EqOp(t, reply.ID, read.GetResult().GetId())
	})

	T.Run("refuses somebody else's comment under the default authorizer", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		comment := h.seed(t, testScope, &comments.Comment{Target: testTarget, Author: otherUser})

		_, err := h.server.ArchiveComment(h.ctx(t), &commentspb.ArchiveCommentRequest{CommentId: comment.ID})

		mustBeCode(t, err, codes.NotFound)
		test.ErrorIs(t, err, commentsgrpc.ErrTargetNotPermitted)
	})

	T.Run("refuses a comment already archived", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		comment := h.seed(t, testScope, &comments.Comment{Target: testTarget})

		_, err := h.server.ArchiveComment(h.ctx(t), &commentspb.ArchiveCommentRequest{CommentId: comment.ID})
		must.NoError(t, err)

		_, err = h.server.ArchiveComment(h.ctx(t), &commentspb.ArchiveCommentRequest{CommentId: comment.ID})

		mustBeCode(t, err, codes.NotFound)
		test.ErrorIs(t, err, comments.ErrCommentNotFound)
	})

	T.Run("refuses a comment in another tenant's scope as absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		comment := h.seed(t, otherScope, &comments.Comment{Target: testTarget})

		_, err := h.server.ArchiveComment(h.ctx(t), &commentspb.ArchiveCommentRequest{CommentId: comment.ID})

		mustBeCode(t, err, codes.NotFound)
		test.ErrorIs(t, err, comments.ErrCommentNotFound)

		// And the neighbor still has it.
		read, err := h.server.GetComment(h.otherCtx(t), &commentspb.GetCommentRequest{CommentId: comment.ID})
		must.NoError(t, err)
		test.EqOp(t, comment.ID, read.GetResult().GetId())
	})
}

// mustBeCode asserts the status a client reads off err.
func mustBeCode(tb testing.TB, err error, code codes.Code) {
	tb.Helper()

	must.Error(tb, err)
	test.EqOp(tb, code, status.Code(err), test.Sprintf("the status was %q", status.Code(err)))
}
