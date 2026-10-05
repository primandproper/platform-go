package comments

import (
	"context"
	"maps"
	"slices"
	"strconv"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/recording"
	"github.com/primandproper/platform-go/v14/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ResourceTypeComment is what an audit entry about a Comment names. It is
// platform's vocabulary for platform's own table, prefixed so a consumer's
// "comment" is never mistaken for it.
const ResourceTypeComment = "comments.comment"

// The events this package's writes emit. They are platform's names for
// platform's own writes, as waitlists' are.
//
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type
// left out of it is still published to the outbox and dispatched to nobody.
//
// A sweep of a target's comments emits none. See
// [RecordingHooks.AfterDeleteCommentsForTarget] for why.
const (
	// EventCommentCreated says somebody commented, or replied.
	EventCommentCreated webhooks.EventType = "comments.comment.created"
	// EventCommentUpdated says a comment's body was edited.
	EventCommentUpdated webhooks.EventType = "comments.comment.updated"
	// EventCommentArchived says a comment was removed from its discussion.
	EventCommentArchived webhooks.EventType = "comments.comment.archived"
	// EventCommentsErased says an author's comments were deleted by an
	// erasure. The payload carries the count, and nothing that identifies
	// them.
	EventCommentsErased webhooks.EventType = "comments.comments.erased"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with:
//
//	catalog := webhooks.Catalog{OrderCreated: {Description: "..."}}
//	maps.Copy(catalog, comments.EventCatalog())
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventCommentCreated:  {Description: "Somebody commented, or replied to a comment."},
		EventCommentUpdated:  {Description: "A comment's body was edited."},
		EventCommentArchived: {Description: "A comment was removed from its discussion."},
		EventCommentsErased:  {Description: "An author's comments were deleted by an erasure."},
	}
}

// CommentEvent is the payload of every comment event.
//
// It names the comment, the thing it is about, the comment it replies to and
// its author, and not the body. The body is what the person said, and an event
// is copied into every subscriber's logs; a subscriber that needs it reads the
// comment while it is still there.
type CommentEvent struct {
	_ struct{} `json:"-"`

	// Target is what the comment is about.
	Target Target `json:"target"`
	// CommentID is the comment the event is about.
	CommentID string `json:"commentID"`
	// ParentID is the comment it replies to, and empty for a root.
	ParentID string `json:"parentID,omitempty"`
	// Author is who wrote it. On an archive it is still the author, and not
	// whoever removed it; the audit entry's actor is that.
	Author string `json:"author"`

	// Changed names the fields an edit moved, sorted, and is empty for every
	// other event. The timestamp every save stamps is left off.
	Changed []string `json:"changed,omitempty"`
}

// ErasureEvent is the payload of EventCommentsErased. It says how many comments
// an erasure deleted, and not whose, for the reason waitlists.ErasureEvent
// gives: "forget this person" is one signal per subject, and it is
// dataprivacy's to emit rather than every store's.
type ErasureEvent struct {
	_ struct{} `json:"-"`

	// Deleted is how many comments the erasure deleted, zero included.
	Deleted int64 `json:"deleted"`
}

// The metadata keys an audit entry here carries.
const (
	metadataTargetType = "targetType"
	metadataTargetID   = "targetID"
	metadataParentID   = "parentID"
	metadataDeleted    = "deleted"
)

// lastUpdatedAtField is the json name of the timestamp every edit stamps, which
// a diff therefore always names and a changed-fields list should not.
const lastUpdatedAtField = "lastUpdatedAt"

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. Every write but a target's sweep
// records an audit entry naming the comment and emits the event above for it,
// both on the write's transaction, through the recording.Recorder it is built
// with.
//
// It implements Hooks outright rather than embedding NoopHooks, so a write
// added to Store later fails to compile here until somebody decides what it
// records. A consumer that wants one entry shaped differently embeds it and
// overrides that method.
//
// Where an entry is filed and who made it are the Recorder's. Every comment
// entry sets Entry.SubjectID to the comment's author, so a Recorder filing by
// subject can; none names the author in its metadata, for the reason
// waitlists.RecordingHooks gives.
//
// Nor does it redact. An edit's diff carries the body, old and new, under the
// field name "body". Whether what somebody said is personal data is the
// deployment's call, made with audit.WithRedaction for [ResourceTypeComment] on
// the audit recorder — a deployment that treats it as personal text hashes it
// there.
type RecordingHooks struct {
	recorder *recording.Recorder
}

var _ Hooks = (*RecordingHooks)(nil)

// NewRecordingHooks builds the Hooks that record every write through recorder.
func NewRecordingHooks(recorder *recording.Recorder) (*RecordingHooks, error) {
	if recorder == nil {
		return nil, ErrNilRecorder
	}

	return &RecordingHooks{recorder: recorder}, nil
}

// AfterCreateComment records somebody commenting.
func (h *RecordingHooks) AfterCreateComment(ctx context.Context, tx database.Tx, scope tenancy.Scope, comment *Comment) error {
	if comment == nil {
		return ErrNilComment
	}

	return h.record(ctx, tx, scope, comment, audit.EventCreated, EventCommentCreated, nil)
}

// AfterUpdateComment records an edit. The entry carries the diff, the body old
// and new; the event carries only the names of the fields that moved.
func (h *RecordingHooks) AfterUpdateComment(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Comment) error {
	if before == nil || after == nil {
		return ErrNilComment
	}

	changes, err := audit.Diff(before, after)
	if err != nil {
		return platformerrors.Wrap(err, "diffing the updated comment")
	}

	return h.record(ctx, tx, scope, after, audit.EventUpdated, EventCommentUpdated, changes)
}

// AfterArchiveComment records a comment being removed from its discussion. The
// entry and the event name the author the row still says, not whoever removed
// it; who removed it is the entry's actor, which the Recorder reads off the
// context. A moderator's removal and an author's own are told apart by
// comparing the two.
func (h *RecordingHooks) AfterArchiveComment(ctx context.Context, tx database.Tx, scope tenancy.Scope, comment *Comment) error {
	if comment == nil {
		return ErrNilComment
	}

	return h.record(ctx, tx, scope, comment, audit.EventArchived, EventCommentArchived, nil)
}

// AfterDeleteCommentsForTarget records nothing.
//
// It is not an erasure. A sweep runs because the thing being discussed was
// removed, and the write that removed it is the consumer's: that write is the
// fact, and it is the one that records the removal, in the consumer's own
// vocabulary for the thing. An entry here would be a second record of the same
// event naming only its side effect, and an event here would tell subscribers
// about the discussion before or after the thing it was about, in an order
// neither write controls.
func (*RecordingHooks) AfterDeleteCommentsForTarget(context.Context, database.Tx, tenancy.Scope, Target, int64) error {
	return nil
}

// AfterDeleteCommentsByAuthor records an erasure: that it ran and how many
// comments it deleted. Zero is recorded too, because the erasure ran. It names
// no author, for the reason
// waitlists.RecordingHooks.AfterWithdrawSignupsForSubject gives: this entry is
// about the erasure and survives it.
func (h *RecordingHooks) AfterDeleteCommentsByAuthor(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	_ string,
	deleted int64,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeComment,
		EventType:    audit.EventDeleted,
		Metadata:     map[string]string{metadataDeleted: strconv.FormatInt(deleted, 10)},
	}

	event := &webhooks.Event{
		EventType: EventCommentsErased,
		Payload:   &ErasureEvent{Deleted: deleted},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// record writes the entry and the event for a write to one comment. The entry's
// SubjectID is the comment's author, so a Recorder filing by subject can.
func (h *RecordingHooks) record(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	comment *Comment,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
	changes map[string]audit.Change,
) error {
	metadata := map[string]string{
		metadataTargetType: comment.Target.Type.String(),
		metadataTargetID:   comment.Target.ID,
	}

	if !comment.Root() {
		metadata[metadataParentID] = comment.ParentID
	}

	entry := &recording.Entry{
		ResourceType: ResourceTypeComment,
		ResourceID:   comment.ID,
		SubjectID:    comment.Author,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata:     metadata,
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: comment.ID,
		Payload: &CommentEvent{
			CommentID: comment.ID,
			Target:    comment.Target,
			ParentID:  comment.ParentID,
			Author:    comment.Author,
			Changed:   changedFields(changes),
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// changedFields names the fields a diff says moved, sorted, without the
// timestamp every save stamps. It is nil for a nil diff, so an event for a
// write that is not an update carries no changed list at all.
func changedFields(changes map[string]audit.Change) []string {
	if len(changes) == 0 {
		return nil
	}

	fields := slices.Sorted(maps.Keys(changes))

	return slices.DeleteFunc(fields, func(field string) bool { return field == lastUpdatedAtField })
}
