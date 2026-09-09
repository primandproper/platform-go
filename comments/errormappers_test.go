package comments_test

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/comments"

	platformerrors "github.com/primandproper/primitives-go/errors"
	httperrors "github.com/primandproper/primitives-go/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// The mappings, spelled once. internal/sentinelmatrix already checks that every
// exported sentinel here is decided about; what this file adds is what each one
// was decided to be, which is the part a reader of an API changes their client
// over.
func TestMappers(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		err      error
		httpMsg  string
		httpCode httperrors.ErrorCode
		grpcCode codes.Code
	}{
		"no such comment": {
			err:      comments.ErrCommentNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such comment",
			grpcCode: codes.NotFound,
		},
		"the comment being replied to is gone": {
			err:      comments.ErrParentNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "the comment being replied to is no longer there",
			grpcCode: codes.NotFound,
		},
		"the thing being commented on is gone": {
			err:      comments.ErrTargetNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "the thing being commented on is no longer there",
			grpcCode: codes.NotFound,
		},
		"a target type outside the catalog": {
			err:      comments.ErrUnknownTargetType,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "comments are not accepted on that kind of thing",
			grpcCode: codes.InvalidArgument,
		},
		"a reply to a reply": {
			err:      comments.ErrNestedReply,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a comment reply may not itself be replied to",
			grpcCode: codes.InvalidArgument,
		},
		"a reply filed under a different discussion": {
			err:      comments.ErrTargetMismatch,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a reply belongs to the same discussion as the comment it replies to",
			grpcCode: codes.InvalidArgument,
		},
		"a comment with nothing in it": {
			err:      comments.ErrEmptyBody,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a comment needs something in it",
			grpcCode: codes.InvalidArgument,
		},
		"a read of replies naming no parent": {
			err:      comments.ErrEmptyParent,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "naming which comment's replies are wanted is required",
			grpcCode: codes.InvalidArgument,
		},
		"a target naming no kind of thing": {
			err:      comments.ErrEmptyTargetType,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a comment must name both what kind of thing it is about and which one",
			grpcCode: codes.InvalidArgument,
		},
		"a target naming no one of them": {
			err:      comments.ErrEmptyTargetID,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a comment must name both what kind of thing it is about and which one",
			grpcCode: codes.InvalidArgument,
		},
		"a comment written by nobody": {
			err:      comments.ErrEmptyAuthor,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a comment must name who wrote it",
			grpcCode: codes.InvalidArgument,
		},
		"a comment written into a scope it does not name": {
			err:      comments.ErrScopeMismatch,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the comment does not belong to that scope",
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := comments.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := comments.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "writing comment")

			_, _, ok = comments.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = comments.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

// TestTheTwoMappersCoverTheSameSentinels is why a service exposing both
// transports answers one refusal the same way on either.
//
// Without it a sentinel added to one switch and not the other would come back
// considered on the transport somebody happened to test and codes.Unknown on the
// other, and which one a client got would depend on how it connected.
func TestTheTwoMappersCoverTheSameSentinels(T *testing.T) {
	T.Parallel()

	for _, err := range []error{
		comments.ErrCommentNotFound,
		comments.ErrParentNotFound,
		comments.ErrTargetNotFound,
		comments.ErrUnknownTargetType,
		comments.ErrNestedReply,
		comments.ErrTargetMismatch,
		comments.ErrEmptyBody,
		comments.ErrEmptyParent,
		comments.ErrEmptyTargetType,
		comments.ErrEmptyTargetID,
		comments.ErrEmptyAuthor,
		comments.ErrScopeMismatch,
	} {
		_, _, claimedByHTTP := comments.HTTPMapper.Map(err)
		_, claimedByGRPC := comments.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

// TestTheClientSafeSentinelsAreTheOnesAPersonCanActOn: each is something
// somebody did and can undo, and none of them names a person, a tenant, or a row
// somebody else owns.
func TestTheClientSafeSentinelsAreTheOnesAPersonCanActOn(T *testing.T) {
	T.Parallel()

	must.SliceLen(T, 6, comments.ClientSafeSentinels)

	for _, err := range comments.ClientSafeSentinels {
		// Every one of them is mapped, because a sentinel quoted verbatim under
		// codes.Unknown would be a sentence attached to the wrong status.
		_, _, ok := comments.HTTPMapper.Map(err)
		test.True(T, ok, test.Sprintf("%v is client-safe and unmapped", err))
	}

	// The absence that matters: a comment absent, archived, and in another
	// tenant's scope are one answer, and quoting the sentinel would not change
	// that — but the list is where somebody would add it by reflex, so it is
	// asserted rather than described.
	for _, err := range comments.ClientSafeSentinels {
		test.False(T, errors.Is(err, comments.ErrCommentNotFound), test.Sprint(
			"ErrCommentNotFound is client-safe; it is the answer a comment in another tenant's scope gets"))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := comments.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := comments.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		// The platform mappers already answer these, so a case here would be a
		// second copy of that decision — and the second copy is the one that can
		// drift. All three are a nil argument inside the process rather than
		// anything a request can express.
		for _, err := range []error{
			comments.ErrNilComment,
			comments.ErrNilExecutor,
			comments.ErrNilDatabaseClient,
		} {
			_, _, ok := comments.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = comments.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})
}
