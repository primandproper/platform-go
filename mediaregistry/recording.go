package mediaregistry

import (
	"context"
	"strconv"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/recording"
	"github.com/primandproper/platform-go/v14/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ResourceTypeObject is what an audit entry about an Object names. It is
// platform's vocabulary for platform's own table, prefixed so a consumer's
// "object" is never mistaken for one of these.
const ResourceTypeObject = "mediaregistry.object"

// The events this package's writes emit, one per write. They are platform's
// names for platform's own writes, which is what makes them constants here
// rather than strings a consumer mints.
//
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type left
// out of it is still published to the outbox and dispatched to nobody.
const (
	// EventObjectRecorded says an upload was registered.
	EventObjectRecorded webhooks.EventType = "mediaregistry.object.recorded"
	// EventObjectArchived says an upload was hidden. The bytes are still in the
	// bucket, and the payload carries the key they are at.
	EventObjectArchived webhooks.EventType = "mediaregistry.object.archived"
	// EventObjectsErased says an owner's uploads were archived by an erasure.
	// The payload carries the count and nothing that identifies the owner.
	EventObjectsErased webhooks.EventType = "mediaregistry.objects.erased"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with:
//
//	maps.Copy(catalog, mediaregistry.EventCatalog())
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventObjectRecorded: {Description: "An uploaded object was registered."},
		EventObjectArchived: {Description: "A registered object was archived; its bytes remain until retention removes them."},
		EventObjectsErased:  {Description: "An owner's registered objects were archived by an erasure."},
	}
}

// ObjectEvent is the payload of EventObjectRecorded and EventObjectArchived.
//
// It carries what a subscriber needs to act on the bytes without a read: the
// key they are at and the owner they belong to, by opaque ID. An archived
// object's key is the one fact a subscriber removing the bytes cannot get back
// from the registry, whose reads no longer see the row.
type ObjectEvent struct {
	_ struct{} `json:"-"`

	// BelongsTo is what the object hangs off, if anything.
	BelongsTo Subject `json:"belongsTo"`
	// ObjectID is the row the event is about.
	ObjectID string `json:"objectID"`
	// OwnerID is whose upload it is.
	OwnerID string `json:"ownerID"`
	// Key is where the bytes are.
	Key string `json:"key"`
	// ContentType is what the bytes are, as stored.
	ContentType string `json:"contentType"`
	// Size is how many bytes were stored.
	Size int64 `json:"size"`
}

// ErasureEvent is the payload of EventObjectsErased. It says how many objects an
// erasure archived, and not whose: "forget this person" is one signal per
// subject, and a per-store erasure event naming them would repeat it once for
// every store they touched. See waitlists.ErasureEvent, which makes the same
// call for the same reason.
//
// Unlike that one it names no kind of subject, because the registry has none to
// name: an owner is whatever the consumer's authorization model calls a
// principal, and the column says nothing about which.
type ErasureEvent struct {
	_ struct{} `json:"-"`

	// Archived is how many objects the erasure archived, zero included.
	Archived int64 `json:"archived"`
}

// The metadata keys an audit entry here carries, read back by whoever reads the
// log.
const (
	metadataContentType   = "contentType"
	metadataSize          = "size"
	metadataBelongsToType = "belongsToType"
	metadataArchived      = "archived"
)

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. Every write records an audit entry
// and emits the event above for it, both on the write's transaction, through
// the recording.Recorder it is built with.
//
// It implements Hooks outright rather than embedding NoopHooks, so a write
// added to Store later fails to compile here until somebody decides what it
// records. That makes it the type a consumer embeds in turn: one that wants a
// single entry shaped differently overrides that method and inherits the rest.
//
// An object's entry names its owner through Entry.SubjectID and nowhere else,
// so a Recorder filing by subject puts it on the owner's chain and the default
// files it where the write ran. Neither the owner nor the key is copied into
// the metadata: audit.Erasure counts and deletes by actor, resource and scope
// and never reads metadata, so an identifier there outlives the erasure of the
// person it names, and a key is often built from one. What the BelongsTo
// subject is, is recorded by type and not by ID for the same reason; it is a
// consumer's noun, and it is sometimes a person.
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

// AfterRecordObject records an upload being registered.
func (h *RecordingHooks) AfterRecordObject(ctx context.Context, tx database.Tx, scope tenancy.Scope, object *Object) error {
	if object == nil {
		return ErrNilObject
	}

	return h.recordObject(ctx, tx, scope, object, audit.EventCreated, EventObjectRecorded)
}

// AfterArchiveObject records an upload being hidden.
//
// The row is the one the archive left, read back through the statement that
// still sees archived rows, so the entry takes whose object it was as its
// subject and the event names the key its bytes are still at, without a read of
// its own: by the
// time anybody would make one, every read but that statement has stopped
// seeing the row.
func (h *RecordingHooks) AfterArchiveObject(ctx context.Context, tx database.Tx, scope tenancy.Scope, object *Object) error {
	if object == nil {
		return ErrNilObject
	}

	return h.recordObject(ctx, tx, scope, object, audit.EventArchived, EventObjectArchived)
}

// AfterArchiveObjectsForOwner records an erasure: that it ran, and how many
// objects it archived. Zero is recorded too, because the erasure ran.
//
// It names no owner, and the reason is waitlists' AfterWithdrawSignupsForSubject's:
// this entry is about the erasure, so it is filed under the write's scope and
// survives every scope deletion, and an owner identifier on it would be the one
// reference the erasure left behind with nothing to say so. The count is what a
// reader of the log can be told.
func (h *RecordingHooks) AfterArchiveObjectsForOwner(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	_ string,
	archived int64,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeObject,
		EventType:    audit.EventArchived,
		Metadata:     map[string]string{metadataArchived: strconv.FormatInt(archived, 10)},
	}

	event := &webhooks.Event{
		EventType: EventObjectsErased,
		Payload:   &ErasureEvent{Archived: archived},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// recordObject writes the entry and the event for a write to one object.
func (h *RecordingHooks) recordObject(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	object *Object,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
) error {
	metadata := map[string]string{
		metadataContentType: object.ContentType,
		metadataSize:        strconv.FormatInt(object.Size, 10),
	}

	if object.BelongsTo.Attached() {
		metadata[metadataBelongsToType] = object.BelongsTo.Type
	}

	entry := &recording.Entry{
		ResourceType: ResourceTypeObject,
		ResourceID:   object.ID,
		SubjectID:    object.OwnerID,
		EventType:    auditEventType,
		Metadata:     metadata,
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: object.ID,
		Payload: &ObjectEvent{
			ObjectID:    object.ID,
			OwnerID:     object.OwnerID,
			BelongsTo:   object.BelongsTo,
			Key:         object.Key,
			ContentType: object.ContentType,
			Size:        object.Size,
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}
