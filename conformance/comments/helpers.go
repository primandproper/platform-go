package comments

import (
	"testing"

	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// target is a thing to comment on that nothing else in the run has commented
// on: the subject's target type, and an identifier minted for this test.
//
// The identifier is minted because a listing by target reads everything anybody
// has said about that target, and in a deployment the suite does not own a fixed
// identifier would be a discussion other people are in.
func target(t *testing.T, s *conformance.Session) *commentspb.CommentTarget {
	t.Helper()

	targetType := s.Seams().CommentTargetType
	if targetType == "" {
		t.Skip("conformance: this subject names no comment target type (Seams.CommentTargetType), " +
			"and every comment is about a thing of some type")
	}

	return &commentspb.CommentTarget{Type: targetType, Id: "conf_" + identifiers.New()}
}

// say starts a discussion on about as caller, failing the test if it is refused.
func say(t *testing.T, caller *conformance.Subject, about *commentspb.CommentTarget, body string) *commentspb.Comment {
	t.Helper()

	created, err := caller.Surfaces.Comments.CreateComment(caller.Context(t.Context()), &commentspb.CreateCommentRequest{
		Comment: &commentspb.CommentInput{Target: about, Body: body},
	})
	must.NoError(t, err, must.Sprintf("commenting %q", body))
	must.NotNil(t, created.GetResult(), must.Sprint("a comment was stored and none came back"))

	return created.GetResult()
}

// reply answers parentID as caller, naming no target, which is what a reply
// form sends: the discussion is the parent's.
func reply(t *testing.T, caller *conformance.Subject, parentID, body string) *commentspb.Comment {
	t.Helper()

	created, err := caller.Surfaces.Comments.CreateComment(caller.Context(t.Context()), &commentspb.CreateCommentRequest{
		Comment: &commentspb.CommentInput{ParentId: parentID, Body: body},
	})
	must.NoError(t, err, must.Sprintf("replying %q", body))
	must.NotNil(t, created.GetResult(), must.Sprint("a reply was stored and none came back"))

	return created.GetResult()
}

// read fetches one comment as caller, failing the test if it is not there.
func read(t *testing.T, caller *conformance.Subject, id string) *commentspb.Comment {
	t.Helper()

	found, err := caller.Surfaces.Comments.GetComment(caller.Context(t.Context()),
		&commentspb.GetCommentRequest{CommentId: id})
	must.NoError(t, err, must.Sprintf("reading comment %q", id))
	must.NotNil(t, found.GetResult())

	return found.GetResult()
}

// roots lists a target's top-level comments as caller.
func roots(t *testing.T, caller *conformance.Subject, about *commentspb.CommentTarget) *commentspb.ListRootCommentsResponse {
	t.Helper()

	page, err := caller.Surfaces.Comments.ListRootComments(caller.Context(t.Context()),
		&commentspb.ListRootCommentsRequest{Target: about})
	must.NoError(t, err, must.Sprint("listing a target's discussion"))

	return page
}

// replies lists one root's replies as caller.
func replies(t *testing.T, caller *conformance.Subject, about *commentspb.CommentTarget, parentID string) []string {
	t.Helper()

	page, err := caller.Surfaces.Comments.ListReplies(caller.Context(t.Context()),
		&commentspb.ListRepliesRequest{Target: about, ParentId: parentID})
	must.NoError(t, err, must.Sprintf("listing the replies to %q", parentID))

	return commentIDs(page.GetResults())
}

// byTargetType lists everything said about things of one type, skipping where
// the caller may not.
//
// It is the moderation read and carries a grant of its own, which a deployment
// enforcing method grants may well withhold from an ordinary caller. That
// refusal is the deployment being right rather than the surface being wrong,
// so the assertion that needed the read skips rather than failing.
func byTargetType(t *testing.T, caller *conformance.Subject, targetType string) []string {
	t.Helper()

	page, err := caller.Surfaces.Comments.ListCommentsByTargetType(caller.Context(t.Context()),
		&commentspb.ListCommentsByTargetTypeRequest{TargetType: targetType})
	if status.Code(err) == codes.PermissionDenied {
		t.Skip("conformance: this caller may not make the moderation read, and the subject mints no caller who may")
	}

	must.NoError(t, err, must.Sprintf("listing everything said about %q", targetType))

	return commentIDs(page.GetResults())
}

// byAuthor lists one author's comments as caller. An empty author is the
// caller's own, which is what a "your comments" page sends.
func byAuthor(t *testing.T, caller *conformance.Subject, author string) ([]string, error) {
	t.Helper()

	page, err := caller.Surfaces.Comments.ListCommentsByAuthor(caller.Context(t.Context()),
		&commentspb.ListCommentsByAuthorRequest{Author: author})
	if err != nil {
		return nil, err
	}

	return commentIDs(page.GetResults()), nil
}

// refused asserts err is a refusal a client reads as code.
func refused(t *testing.T, err error, code codes.Code, what string) {
	t.Helper()

	must.Error(t, err, must.Sprintf("%s was not refused", what))
	test.EqOp(t, code, status.Code(err), test.Sprintf("%s was refused with the wrong code", what))
}

// saying asserts a refusal's message carries words, for the refusals this
// surface lets a client show to the person who made them.
//
// The words rather than the whole sentence, because the sentence is the
// sentinel's and may be reworded; that the reason reaches the client at all is
// the promise, since the code alone does not say which field to fix.
func saying(t *testing.T, err error, words string) {
	t.Helper()

	test.StrContains(t, status.Convert(err).Message(), words,
		test.Sprint("the refusal did not tell the client why"))
}

// needsUser skips unless the subject surfaced the caller's user identifier.
func needsUser(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.UserID == "" {
		t.Skip("conformance: this subject does not surface the caller's user identifier")
	}
}

// twoTenants mints two callers and refuses to proceed if the subject put them in
// one tenant, since every confinement assertion here would then compare a
// tenant with itself and pass.
func twoTenants(t *testing.T, s *conformance.Session) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.Subject(t), s.Subject(t)

	must.StrNotEqFold(t, mine.Scope.String(), theirs.Scope.String(),
		must.Sprint("the subject minted two callers in one tenant; the confinement this asserts cannot be observed"))

	return mine, theirs
}

// colleague mints a second caller in of's tenant. A subject that cannot put two
// callers in one tenant declines, and the assertion that asked skips.
func colleague(t *testing.T, s *conformance.Session, of *conformance.Subject) *conformance.Subject {
	t.Helper()

	needsUser(t, of)

	other := s.Subject(t, conformance.InTenant(of.Scope))
	needsUser(t, other)

	must.StrNotEqFold(t, of.UserID, other.UserID,
		must.Sprint("the subject minted a colleague as the same user"))

	return other
}

func commentIDs(results []*commentspb.Comment) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		out = append(out, result.GetId())
	}

	return out
}
