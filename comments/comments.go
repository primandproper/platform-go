package comments

import (
	"time"

	"github.com/primandproper/primitives-go/tenancy"
)

// The keys this package attaches to spans and log lines. Declared once so a
// trace and a log line name the same fact the same way.
const (
	// serviceName scopes this package's spans, logger, and instruments.
	serviceName = "comments"

	scopeKey      = serviceName + ".scope"
	commentIDKey  = serviceName + ".comment_id"
	parentIDKey   = serviceName + ".parent_id"
	authorKey     = serviceName + ".author"
	targetTypeKey = serviceName + ".target_type"
	targetIDKey   = serviceName + ".target_id"
	countKey      = serviceName + ".count"
)

// AuthorAttributeKey is the metric and span attribute a caller labels its own
// instruments with when the thing being measured is about one author. It is
// exported so a consumer's attributes agree with this package's rather than
// merely resembling them.
const AuthorAttributeKey = authorKey

// RootParentID is the parent a comment that replies to nothing has: none.
//
// It is exported because it is a stored value rather than an internal sentinel —
// it is what parent_id holds for a root, and it is what [Store.ListRootComments]
// binds. A caller assembling a thread from a page of comments compares against
// it rather than against a literal "".
//
// It is the empty string rather than NULL for a reason that shows up in the SQL:
// "the roots of this target" is then an equality against a bound value, which
// makes the root list and the reply list one statement instead of two. See
// comments/internal/queries.
const RootParentID = ""

// Comment is something somebody said, about something the application owns,
// possibly in reply to something else somebody said.
//
// It is deliberately app-independent. Target is how the application names the
// thing being discussed, and the catalog the store was built with is what says
// which types exist; Author is a string because this package does not own the
// directory people live in. What this package owns is everything around them —
// the scope, the thread, the page, the erasure — which is the half that is the
// same in every application and the half every application otherwise writes
// again.
type Comment struct {
	// CreatedAt is when the comment was written, assigned by the database.
	CreatedAt time.Time `json:"createdAt"`
	// LastUpdatedAt is when the body was last edited, and nil for a comment
	// nobody has revised.
	//
	// It is what a client renders an "edited" marker from. That reading holds
	// because the edit is the only write that revises a live comment: the target,
	// the parent and the author are not editable facts, and the archive takes the
	// row out of every list rather than changing what it says.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt,omitempty"`
	// ArchivedAt is when the comment was removed from the discussion, and nil
	// while it is still in it.
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	// ID identifies the comment.
	ID string `json:"id"`
	// ParentID is the comment this one replies to, and RootParentID — the empty
	// string — is a comment that replies to nothing.
	//
	// Replies are one level deep: a comment whose parent is itself a reply is
	// refused. See the package documentation for why, and for what a client
	// renders when a reply's parent is no longer there.
	ParentID string `json:"parentID,omitempty"`
	// Author is who wrote it — a user id in every deployment this module has,
	// but a string here, because comments does not own the directory.
	Author string `json:"author"`
	// Body is what the person actually said.
	Body string `json:"body"`
	// Target is what the comment is about.
	Target Target `json:"target"`
	// Scope is whose data this is.
	Scope tenancy.Scope `json:"scope"`
}

// Root reports whether the comment replies to nothing — the top level of a
// discussion, and the only thing a reply may be a reply to.
func (c *Comment) Root() bool { return c != nil && c.ParentID == RootParentID }
