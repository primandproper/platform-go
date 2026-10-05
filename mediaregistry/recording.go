package mediaregistry

import (
	"context"
	"strconv"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

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

// The metadata keys an audit entry here carries, read back by whoever reads the
// log.
const (
	metadataContentType   = "contentType"
	metadataSize          = "size"
	metadataBelongsToType = "belongsToType"
)

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. A registration and an archive each
// record an audit entry and emit the event above for it, both on the write's
// transaction, through the recording.Recorder it is built with. An erasure
// records nothing, for the reason AfterArchiveObjectsForOwner gives.
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

// AfterArchiveObjectsForOwner records nothing, deliberately.
//
// Store.ArchiveObjectsForOwner is an erasure's write, one table of the many a
// single erasure request reaches, and dataprivacy.Fulfiller records that request
// once: one audit entry naming it, with this table's count among the
// per-section counts in its metadata, and one dataprivacy.EventErasureFulfilled.
// An entry and an event here as well would be the request's fan-out across
// stores written down as a separate fact per store, none of which says which
// erasure it belonged to.
//
// A consumer that calls Store.ArchiveObjectsForOwner outside an erasure has an
// archive nothing else recorded, and embeds this type to override the method.
func (h *RecordingHooks) AfterArchiveObjectsForOwner(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
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
