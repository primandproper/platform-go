package grpc_test

import (
	"context"
	"testing"

	commentsgrpc "github.com/primandproper/platform-go/v14/comments/grpc"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
)

// errAuthorizerUnavailable stands in for a rule that could not be evaluated —
// the membership read a consumer's authorizer makes, against a database that is
// not answering.
var errAuthorizerUnavailable = platformerrors.New("the moderation rule could not be evaluated")

// permitAll is the consumer who has moderators and has decided this caller is
// one.
type permitAll struct{}

var _ commentsgrpc.AuthorAuthorizer = permitAll{}

func (permitAll) AuthorizeAuthor(context.Context, commentsgrpc.Principal, string) error { return nil }

// brokenAuthorizer is the third answer the seam distinguishes: not permitted,
// not refused, could not decide.
type brokenAuthorizer struct{}

var _ commentsgrpc.AuthorAuthorizer = brokenAuthorizer{}

func (brokenAuthorizer) AuthorizeAuthor(context.Context, commentsgrpc.Principal, string) error {
	return errAuthorizerUnavailable
}

// TestOwnCommentsOnly pins the default rule, which is the one a consumer who
// says nothing gets.
func TestOwnCommentsOnly(T *testing.T) {
	T.Parallel()

	caller := &testPrincipal{userID: testUser, scope: testScope}

	T.Run("permits the caller's own comments", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, commentsgrpc.OwnCommentsOnly{}.AuthorizeAuthor(t.Context(), caller, testUser))
	})

	T.Run("refuses everybody else's", func(t *testing.T) {
		t.Parallel()

		err := commentsgrpc.OwnCommentsOnly{}.AuthorizeAuthor(t.Context(), caller, otherUser)

		test.ErrorIs(t, err, commentsgrpc.ErrTargetNotPermitted)
	})

	// The refusal is identity/grpc's value, so a consumer running both writes
	// one rule and one errors.Is.
	T.Run("refuses with the directory's own sentinel", func(t *testing.T) {
		t.Parallel()

		err := commentsgrpc.OwnCommentsOnly{}.AuthorizeAuthor(t.Context(), caller, otherUser)

		test.ErrorIs(t, err, identitygrpc.ErrTargetNotPermitted)
	})

	// An empty author is nobody rather than everybody: the server resolves an
	// empty request field to the caller before asking, so a rule that treated
	// "" as a match would permit a caller with no identifier to read what a
	// storeful of comments attributed to nobody says.
	T.Run("refuses an author nobody is", func(t *testing.T) {
		t.Parallel()

		anonymous := &testPrincipal{scope: testScope}

		err := commentsgrpc.OwnCommentsOnly{}.AuthorizeAuthor(t.Context(), anonymous, "")

		test.ErrorIs(t, err, commentsgrpc.ErrTargetNotPermitted)
	})

	T.Run("refuses a caller who is nobody at all", func(t *testing.T) {
		t.Parallel()

		err := commentsgrpc.OwnCommentsOnly{}.AuthorizeAuthor(t.Context(), nil, testUser)

		test.ErrorIs(t, err, commentsgrpc.ErrTargetNotPermitted)
	})
}

// TestAuthorAuthorizerFunc is the one-closure form, for a consumer whose rule is
// something they already hold.
func TestAuthorAuthorizerFunc(T *testing.T) {
	T.Parallel()

	var asked string

	rule := commentsgrpc.AuthorAuthorizerFunc(func(_ context.Context, _ commentsgrpc.Principal, author string) error {
		asked = author

		return nil
	})

	test.NoError(T, rule.AuthorizeAuthor(T.Context(), &testPrincipal{userID: testUser}, otherUser))
	test.EqOp(T, otherUser, asked)
}
