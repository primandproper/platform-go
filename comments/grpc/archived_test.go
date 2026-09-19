package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	commentsgrpc "github.com/primandproper/platform-go/v14/comments/grpc"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The suite's stand-in for the half of a consumer's wiring that says what the
// caller may do, and the four reads' answer to a client that asked for the
// comments a moderator removed.
//
// The extractor is what a deployment hands both this surface and
// primitives-go's authorization/grpc enforcer. It is not the enforcer: nothing
// in these tests checks whether the method may be called at all, because that
// check is an interceptor's and runs before the handler. What is under test is
// the second question — which rows the answer may contain — and it is asked
// inside the handler, where the filter is.

// grantsKey is where this suite puts the caller's authority.
type grantsKey struct{}

// withGrants narrows a request context to exactly the permissions named, which
// is how a test describes a caller who may read and may not archive.
func withGrants(ctx context.Context, perms ...authorization.Permission) context.Context {
	return context.WithValue(ctx, grantsKey{}, authorization.NewGrants(authorization.NewPermissionSet(perms...)))
}

// extractGrants is the authorization.GrantsExtractor every harness here is built
// with.
//
// A context nothing narrowed carries every grant, because the rest of this suite
// is about what a handler does and not about what a policy allows — a default of
// "nothing" would make every existing test a test of this file. A test that cares
// says so with [withGrants].
func extractGrants(ctx context.Context) (authorization.Grants, bool) {
	grants, ok := ctx.Value(grantsKey{}).(authorization.Grants)
	if !ok {
		return authorization.AllowAll(), true
	}

	return grants, true
}

// includeArchived is the filter a client sets to ask for the removed rows.
func includeArchived() *filteringpb.QueryFilter {
	include := true

	return &filteringpb.QueryFilter{IncludeArchived: &include}
}

// archive takes a seeded comment out of the discussion directly through the
// store, so the row a test is about was removed without going through the
// surface the test is about.
func (h *harness) archive(tb testing.TB, comment *comments.Comment) {
	tb.Helper()

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		_, err := h.store.ArchiveComment(tb.Context(), tx, testScope, comment.ID)

		return err
	}))
}

// ids is what every assertion below compares, because the rows' identity is the
// whole of the question: which comments came back.
func ids(results []*commentspb.Comment) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		out = append(out, result.GetId())
	}

	return out
}

// TestIncludeArchivedIsAGrantAndNotAField is the ruling, executed, on all four
// paged reads.
//
// Each case seeds one live comment and one a moderator removed, sends
// include_archived: true, and asks the same question twice: as somebody holding
// only the grant the RPC requires, and as somebody who also holds
// comments.archive. The first gets the discussion, the second gets what was
// taken out of it.
func TestIncludeArchivedIsAGrantAndNotAField(T *testing.T) {
	T.Parallel()

	// The four RPCs, each reduced to "which comment ids does this answer with",
	// so the table below is about the rule rather than about five response
	// shapes.
	reads := map[string]struct {
		read  func(tb testing.TB, h *harness, ctx context.Context) []string
		grant authorization.Permission
	}{
		"ListRootComments": {
			grant: commentsgrpc.PermissionReadComments,
			read: func(tb testing.TB, h *harness, ctx context.Context) []string {
				tb.Helper()

				res, err := h.server.ListRootComments(ctx, &commentspb.ListRootCommentsRequest{
					Target: commentsgrpc.TargetToProto(testTarget),
					Filter: includeArchived(),
				})
				must.NoError(tb, err)

				return ids(res.GetResults())
			},
		},
		"ListCommentsByTargetType": {
			grant: commentsgrpc.PermissionModerateComments,
			read: func(tb testing.TB, h *harness, ctx context.Context) []string {
				tb.Helper()

				res, err := h.server.ListCommentsByTargetType(ctx, &commentspb.ListCommentsByTargetTypeRequest{
					TargetType: string(recipeType),
					Filter:     includeArchived(),
				})
				must.NoError(tb, err)

				return ids(res.GetResults())
			},
		},
		"ListCommentsByAuthor": {
			grant: commentsgrpc.PermissionReadComments,
			read: func(tb testing.TB, h *harness, ctx context.Context) []string {
				tb.Helper()

				res, err := h.server.ListCommentsByAuthor(ctx, &commentspb.ListCommentsByAuthorRequest{
					Filter: includeArchived(),
				})
				must.NoError(tb, err)

				return ids(res.GetResults())
			},
		},
	}

	for name, read := range reads {
		T.Run(name+" hides the removed rows from a caller who cannot archive", func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			live := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "said"})
			removed := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "removed"})
			h.archive(t, removed)

			got := read.read(t, h, withGrants(h.ctx(t), read.grant))

			test.Eq(t, []string{live.ID}, got)
		})

		T.Run(name+" answers the removed rows to a caller who can archive", func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			live := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "said"})
			removed := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "removed"})
			h.archive(t, removed)

			got := read.read(t, h, withGrants(h.ctx(t), read.grant, commentsgrpc.PermissionArchiveComments))

			test.SliceContains(t, got, live.ID)
			test.SliceContains(t, got, removed.ID)
		})
	}

	// ListReplies is the fourth and is separate because it needs a root to reply
	// to, which no other read here does.
	T.Run("ListReplies hides the removed replies from a caller who cannot archive", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		root := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "root"})
		live := h.seed(t, testScope, &comments.Comment{Target: testTarget, ParentID: root.ID, Body: "reply"})
		removed := h.seed(t, testScope, &comments.Comment{Target: testTarget, ParentID: root.ID, Body: "removed"})
		h.archive(t, removed)

		res, err := h.server.ListReplies(
			withGrants(h.ctx(t), commentsgrpc.PermissionReadComments),
			&commentspb.ListRepliesRequest{
				Target:   commentsgrpc.TargetToProto(testTarget),
				ParentId: root.ID,
				Filter:   includeArchived(),
			})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, ids(res.GetResults()))
	})

	T.Run("ListReplies answers the removed replies to a caller who can archive", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		root := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "root"})
		live := h.seed(t, testScope, &comments.Comment{Target: testTarget, ParentID: root.ID, Body: "reply"})
		removed := h.seed(t, testScope, &comments.Comment{Target: testTarget, ParentID: root.ID, Body: "removed"})
		h.archive(t, removed)

		res, err := h.server.ListReplies(
			withGrants(h.ctx(t), commentsgrpc.PermissionReadComments, commentsgrpc.PermissionArchiveComments),
			&commentspb.ListRepliesRequest{
				Target:   commentsgrpc.TargetToProto(testTarget),
				ParentId: root.ID,
				Filter:   includeArchived(),
			})
		must.NoError(t, err)

		got := ids(res.GetResults())
		test.SliceContains(t, got, live.ID)
		test.SliceContains(t, got, removed.ID)
	})

	// The narrowing is not a refusal, which is the half a caller notices: the
	// read succeeds and answers with what they were entitled to ask for.
	T.Run("clearing the field is not an error", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "said"})

		_, err := h.server.ListRootComments(
			withGrants(h.ctx(t), commentsgrpc.PermissionReadComments),
			&commentspb.ListRootCommentsRequest{
				Target: commentsgrpc.TargetToProto(testTarget),
				Filter: includeArchived(),
			})

		must.NoError(t, err)
	})

	// The fail-closed default. A consumer who wired no extractor cannot be told
	// apart from one whose caller holds nothing, so the surface answers the same
	// way rather than guessing.
	T.Run("a server built with no grants extractor clears the field", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, commentsgrpc.WithGrantsExtractor(nil))
		live := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "said"})
		removed := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "removed"})
		h.archive(t, removed)

		res, err := h.server.ListRootComments(h.ctx(t), &commentspb.ListRootCommentsRequest{
			Target: commentsgrpc.TargetToProto(testTarget),
			Filter: includeArchived(),
		})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, ids(res.GetResults()))
	})

	// An extractor that reports no authority is the interceptor's "no grants
	// could be determined", and it is a denial everywhere else in the stack.
	T.Run("an extractor reporting no authority clears the field", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, commentsgrpc.WithGrantsExtractor(
			func(context.Context) (authorization.Grants, bool) { return authorization.AllowAll(), false }))

		live := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "said"})
		removed := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "removed"})
		h.archive(t, removed)

		res, err := h.server.ListRootComments(h.ctx(t), &commentspb.ListRootCommentsRequest{
			Target: commentsgrpc.TargetToProto(testTarget),
			Filter: includeArchived(),
		})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, ids(res.GetResults()))
	})

	// A filter that never asked is left alone, which is what stops the
	// confinement from being a rewrite of everybody's page.
	T.Run("a filter that did not ask is untouched", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		live := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "said"})
		removed := h.seed(t, testScope, &comments.Comment{Target: testTarget, Body: "removed"})
		h.archive(t, removed)

		res, err := h.server.ListRootComments(
			withGrants(h.ctx(t), commentsgrpc.PermissionReadComments, commentsgrpc.PermissionArchiveComments),
			&commentspb.ListRootCommentsRequest{Target: commentsgrpc.TargetToProto(testTarget)})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, ids(res.GetResults()))
	})
}
