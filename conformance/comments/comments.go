package comments

import (
	"testing"

	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/conformance"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "comments"

// The calls this suite makes, as the names a caller is minted to make them by.
const (
	archiveComment           = commentspb.CommentsService_ArchiveComment_FullMethodName
	createComment            = commentspb.CommentsService_CreateComment_FullMethodName
	getComment               = commentspb.CommentsService_GetComment_FullMethodName
	listCommentsByAuthor     = commentspb.CommentsService_ListCommentsByAuthor_FullMethodName
	listCommentsByTargetType = commentspb.CommentsService_ListCommentsByTargetType_FullMethodName
	listReplies              = commentspb.CommentsService_ListReplies_FullMethodName
	listRootComments         = commentspb.CommentsService_ListRootComments_FullMethodName
	updateComment            = commentspb.CommentsService_UpdateComment_FullMethodName
)

// Suite is the discussion surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
		Mounted: func(s conformance.Surfaces) bool { return s.Comments != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("writing", func(t *testing.T) {
		t.Parallel()
		writing(t, s)
	})
	t.Run("reading", func(t *testing.T) {
		t.Parallel()
		reading(t, s)
	})
	t.Run("authorship", func(t *testing.T) {
		t.Parallel()
		authorship(t, s)
	})
	t.Run("confinement", func(t *testing.T) {
		t.Parallel()
		confinement(t, s)
	})
}
