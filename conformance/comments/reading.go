package comments

import (
	"testing"

	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

func reading(t *testing.T, s *conformance.Session) {
	t.Helper()

	// The roots, and only the roots: the replies are a separate question with
	// a read of their own, and a thread view that got both from one call would
	// render every reply twice.
	t.Run("a target's discussion lists its roots without their replies", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		about := target(t, s, caller)
		root := say(t, caller, about, bodyRoot)
		answer := reply(t, caller, root.GetId(), bodyReply)

		page := roots(t, caller, about)
		got := commentIDs(page.GetResults())

		test.SliceContains(t, got, root.GetId(), test.Sprint("a root was missing from its discussion"))
		test.SliceNotContains(t, got, answer.GetId(), test.Sprint("a reply was listed among the roots"))
		test.NotNil(t, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))
	})

	t.Run("a root's replies are listed without the root", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		about := target(t, s, caller)
		root := say(t, caller, about, bodyRoot)
		answer := reply(t, caller, root.GetId(), bodyReply)

		got := replies(t, caller, about, root.GetId())
		test.SliceContains(t, got, answer.GetId(), test.Sprint("a reply was missing from its root's replies"))
		test.SliceNotContains(t, got, root.GetId(), test.Sprint("a root was listed among its own replies"))
	})

	// The empty parent is what a root stores, so answering it with the roots
	// would be the wrong half of the discussion with nothing saying so.
	t.Run("a replies listing naming no parent is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := caller.Surfaces.Comments.ListReplies(caller.Context(t.Context()),
			&commentspb.ListRepliesRequest{Target: target(t, s, caller)})
		refused(t, err, codes.InvalidArgument, "a replies listing with no parent")
	})

	t.Run("the moderation read reaches roots and replies on every target of a type", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		first, second := target(t, s, caller), target(t, s, caller)
		must.EqOp(t, first.GetType(), second.GetType(),
			must.Sprint("the comment target action reported two target types; the moderation read is asserted across one"))

		root := say(t, caller, first, bodyRoot)
		answer := reply(t, caller, root.GetId(), bodyReply)
		elsewhere := say(t, caller, second, "about the other one")

		got := byTargetType(t, caller, first.GetType())
		test.SliceContains(t, got, root.GetId(), test.Sprint("a root was missing from the moderation read"))
		test.SliceContains(t, got, answer.GetId(), test.Sprint("a reply was missing from the moderation read"))
		test.SliceContains(t, got, elsewhere.GetId(),
			test.Sprint("a comment on another target of the same type was missing from the moderation read"))
	})

	// The catalog gates writes and not reads: a type an operator has withdrawn
	// is exactly the one whose rows they still need to reach, so asking after a
	// type nothing accepts comments on is answered rather than refused.
	t.Run("the moderation read answers for a target type nothing accepts comments on", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsTarget(t, s)

		byTargetType(t, caller, "conformance_withdrawn_"+identifiers.New())
	})

	// An empty author is the caller's own, which is what a "your comments" page
	// sends. The colleague's comment is the control that the read is narrowed
	// to the caller rather than answering the whole tenant.
	t.Run("a caller's own comments are listed when the request names nobody", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		other := colleague(t, s, caller)
		about := target(t, s, caller)

		mine := say(t, caller, about, bodyRoot)
		theirs := say(t, other, about, bodyReply)

		got, err := byAuthor(t, caller, "")
		must.NoError(t, err)
		test.SliceContains(t, got, mine.GetId(), test.Sprint("the caller's own comment was missing from their comments"))
		test.SliceNotContains(t, got, theirs.GetId(), test.Sprint("a colleague's comment was listed as the caller's"))
	})

	t.Run("a caller's own comments are listed when the request names them", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsUser(t, caller)

		mine := say(t, caller, target(t, s, caller), bodyRoot)

		got, err := byAuthor(t, caller, caller.UserID)
		must.NoError(t, err)
		test.SliceContains(t, got, mine.GetId())
	})
}
