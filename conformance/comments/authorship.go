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

// authorship is the half of authorization a grant on the method cannot reach.
// A holder of the edit grant may call UpdateComment, and nothing in that says
// whose comment; these ask the second question, about a colleague in the same
// tenant, so the scope is shared and the refusal is the author authorizer's and
// nobody else's.
func authorship(t *testing.T, s *conformance.Session) {
	t.Helper()

	// NotFound rather than PermissionDenied: the comment has already been read
	// by the time whose it is can be asked, so a distinct code would tell a
	// caller walking identifiers which of them are real.
	t.Run("a colleague's comment cannot be edited, and the refusal reads as absence", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		other := colleague(t, s, caller)
		about := target(t, s, caller)

		mine := say(t, caller, about, bodyRoot)
		theirs := say(t, other, about, "as written")

		// The positive control: the caller may edit their own. Without it a
		// surface refusing every edit would pass the refusal below.
		_, err := caller.Surfaces.Comments.UpdateComment(caller.Context(t.Context()),
			&commentspb.UpdateCommentRequest{CommentId: mine.GetId(), Body: "revised"})
		must.NoError(t, err, must.Sprint("the caller cannot edit their own comment; the refusal below proves nothing"))

		_, err = caller.Surfaces.Comments.UpdateComment(caller.Context(t.Context()),
			&commentspb.UpdateCommentRequest{CommentId: theirs.GetId(), Body: "words they did not write"})
		refused(t, err, codes.NotFound, "an edit to a colleague's comment")

		// And the refused edit changed nothing. Reads are not the author's to
		// gate, so the caller can see for themselves.
		got := read(t, caller, theirs.GetId())
		test.EqOp(t, "as written", got.GetBody())
		test.Nil(t, got.GetLastUpdatedAt(), test.Sprint("a refused edit marked the colleague's comment edited"))
		test.EqOp(t, other.UserID, got.GetAuthor())
	})

	t.Run("a colleague's comment cannot be archived, and the refusal reads as absence", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		other := colleague(t, s, caller)
		about := target(t, s, caller)

		mine := say(t, caller, about, bodyRoot)
		theirs := say(t, other, about, bodyReply)

		// The positive control, as above.
		_, err := caller.Surfaces.Comments.ArchiveComment(caller.Context(t.Context()),
			&commentspb.ArchiveCommentRequest{CommentId: mine.GetId()})
		must.NoError(t, err, must.Sprint("the caller cannot archive their own comment; the refusal below proves nothing"))

		_, err = caller.Surfaces.Comments.ArchiveComment(caller.Context(t.Context()),
			&commentspb.ArchiveCommentRequest{CommentId: theirs.GetId()})
		refused(t, err, codes.NotFound, "archiving a colleague's comment")

		// And the colleague's comment is still in the discussion.
		test.SliceContains(t, commentIDs(roots(t, other, about).GetResults()), theirs.GetId(),
			test.Sprint("a refused archive took a colleague's comment out of the discussion"))
	})

	// Refused before anything is read, so the answer is PermissionDenied and
	// says nothing about whether the identifier belongs to anybody — which the
	// next assertion checks from the other side.
	t.Run("a colleague's comments cannot be listed by naming them", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		other := colleague(t, s, caller)
		say(t, other, target(t, s, other), bodyRoot)

		// The positive control: naming themselves is answered.
		_, err := byAuthor(t, caller, caller.UserID)
		must.NoError(t, err, must.Sprint("the caller cannot list their own comments; the refusal below proves nothing"))

		_, err = byAuthor(t, caller, other.UserID)
		refused(t, err, codes.PermissionDenied, "listing a colleague's comments")
	})

	t.Run("an author nobody has heard of is refused exactly as a colleague is", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsUser(t, caller)

		_, err := byAuthor(t, caller, caller.UserID)
		must.NoError(t, err, must.Sprint("the caller cannot list their own comments; the refusal below proves nothing"))

		_, err = byAuthor(t, caller, "conf_nobody_"+identifiers.New())
		refused(t, err, codes.PermissionDenied, "listing an author who does not exist")
	})
}
