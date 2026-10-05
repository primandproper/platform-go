package settings

import (
	"context"
	"maps"
	"slices"
	"strconv"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The resource types this package's audit entries name. They are platform's
// vocabulary for platform's own tables, prefixed so a consumer's "value" is
// never mistaken for one of these.
const (
	// ResourceTypeDefinition is what an audit entry about a Definition names.
	ResourceTypeDefinition = "settings.definition"
	// ResourceTypeValue is what an audit entry about a Value names.
	ResourceTypeValue = "settings.value"
)

// The events this package's writes emit, one per write. They are platform's
// names for platform's own writes, as waitlists' are.
//
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type
// left out of it is still published to the outbox and dispatched to nobody.
const (
	// EventDefinitionCreated says a setting was defined.
	EventDefinitionCreated webhooks.EventType = "settings.definition.created"
	// EventDefinitionUpdated says a setting's definition was rewritten; the
	// payload names which fields.
	EventDefinitionUpdated webhooks.EventType = "settings.definition.updated"
	// EventDefinitionArchived says a setting was retired.
	EventDefinitionArchived webhooks.EventType = "settings.definition.archived"

	// EventValueSet says a subject answered a setting, for the first time or
	// again. The payload names which fields moved when there was an answer
	// before, and none when there was not.
	EventValueSet webhooks.EventType = "settings.value.set"
	// EventValueCleared says a subject withdrew their answer, and resolution
	// falls back to the definition's default.
	EventValueCleared webhooks.EventType = "settings.value.cleared"
	// EventValuesErased says a subject's answers were deleted by an erasure.
	// The payload carries the count and the kind of subject, and nothing that
	// identifies them.
	EventValuesErased webhooks.EventType = "settings.values.erased"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with:
//
//	catalog := webhooks.Catalog{OrderCreated: {Description: "..."}}
//	maps.Copy(catalog, settings.EventCatalog())
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventDefinitionCreated:  {Description: "A setting was defined."},
		EventDefinitionUpdated:  {Description: "A setting's definition changed."},
		EventDefinitionArchived: {Description: "A setting was retired."},
		EventValueSet:           {Description: "A subject answered a setting."},
		EventValueCleared:       {Description: "A subject withdrew their answer to a setting."},
		EventValuesErased:       {Description: "A subject's answers to settings were deleted by an erasure."},
	}
}

// DefinitionEvent is the payload of every definition event. It carries the
// identifiers a subscriber needs to go and read the definition, and for an
// update the names of the fields that moved, rather than the row.
type DefinitionEvent struct {
	_ struct{} `json:"-"`

	// DefinitionID is the definition the event is about.
	DefinitionID string `json:"definitionID"`
	// Name is the setting's name as the write left it, so a subscriber can say
	// which setting without a read.
	Name string `json:"name"`

	// Changed names the fields an update moved, sorted, and is empty for every
	// other event. The timestamp every save stamps is left off.
	Changed []string `json:"changed,omitempty"`
}

// ValueEvent is the payload of every value event.
//
// It names the subject, the setting and the row, and not the answer. The answer
// is the subject's own choice and an event is copied into every subscriber's
// logs; a subscriber that needs it resolves the setting for the subject, which
// also tells it what a cleared answer fell back to.
type ValueEvent struct {
	_ struct{} `json:"-"`

	// Subject is whose answer it is.
	Subject Subject `json:"subject"`
	// ValueID is the row the event is about.
	ValueID string `json:"valueID"`
	// DefinitionID is the setting it answers.
	DefinitionID string `json:"definitionID"`
	// Name is that setting's name.
	Name string `json:"name"`

	// Changed names the fields a revised answer moved, sorted. It is empty for a
	// first answer, for one reviving a cleared answer, and for a clearing.
	Changed []string `json:"changed,omitempty"`
}

// ErasureEvent is the payload of EventValuesErased. It says how many answers an
// erasure deleted and what kind of subject they belonged to, and not whose, for
// the reason waitlists.ErasureEvent gives: "forget this person" is one signal
// per subject, and it is dataprivacy's to emit rather than every store's.
type ErasureEvent struct {
	_ struct{} `json:"-"`

	// SubjectType is the kind of subject erased.
	SubjectType SubjectType `json:"subjectType"`
	// Deleted is how many answers the erasure deleted, zero included.
	Deleted int64 `json:"deleted"`
}

// The metadata keys an audit entry here carries.
const (
	metadataDefinitionID = "definitionID"
	metadataName         = "name"
	metadataSubjectType  = "subjectType"
	metadataDeleted      = "deleted"
)

// The json names of the fields the recording treats specially: the timestamp
// every update stamps, which a diff therefore always names and a changed list
// should not, and the stored answer, which a clearing records as withdrawn.
const (
	lastUpdatedAtField = "lastUpdatedAt"
	rawValueField      = "value"
)

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. Every write records an audit entry
// naming the row and emits the event above for it, both on the write's
// transaction, through the recording.Recorder it is built with.
//
// It implements Hooks outright rather than embedding NoopHooks, so a write
// added to Store later fails to compile here until somebody decides what it
// records. A consumer that wants one entry shaped differently embeds it and
// overrides that method.
//
// Where an entry is filed and who made it are the Recorder's. Every value entry
// sets Entry.SubjectID to the value's subject, so a Recorder filing by subject
// can; none names the subject in its metadata or its diff, for the reason
// waitlists.RecordingHooks gives. A definition entry names nobody: a definition
// is a catalog row.
//
// Nor does it redact. A value's diff carries the answer, old and new, under the
// field name "value"; whether a subject's answer is personal data is the
// deployment's call, made with audit.WithRedaction for [ResourceTypeValue] on
// the audit recorder.
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

// AfterCreateDefinition records a setting being defined.
func (h *RecordingHooks) AfterCreateDefinition(ctx context.Context, tx database.Tx, scope tenancy.Scope, definition *Definition) error {
	if definition == nil {
		return ErrNilDefinition
	}

	return h.recordDefinition(ctx, tx, scope, definition, audit.EventCreated, EventDefinitionCreated, nil)
}

// AfterUpdateDefinition records a definition being rewritten. The entry carries
// the diff; the event carries only the names of the fields that moved.
func (h *RecordingHooks) AfterUpdateDefinition(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Definition) error {
	if before == nil || after == nil {
		return ErrNilDefinition
	}

	changes, err := audit.Diff(before, after)
	if err != nil {
		return platformerrors.Wrap(err, "diffing the updated setting definition")
	}

	return h.recordDefinition(ctx, tx, scope, after, audit.EventUpdated, EventDefinitionUpdated, changes)
}

// AfterArchiveDefinition records a setting being retired. The row is the one
// from before the archive, which is what lets the entry and the event name the
// setting: no read on this package reaches it afterwards.
func (h *RecordingHooks) AfterArchiveDefinition(ctx context.Context, tx database.Tx, scope tenancy.Scope, definition *Definition) error {
	if definition == nil {
		return ErrNilDefinition
	}

	return h.recordDefinition(ctx, tx, scope, definition, audit.EventArchived, EventDefinitionArchived, nil)
}

// AfterSetValue records a subject answering a setting. The entry's diff is
// against the previous answer, and against nothing when there was none — a
// first answer, or one reviving a cleared answer, which the store hands over
// as a nil before for that reason.
func (h *RecordingHooks) AfterSetValue(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	definition *Definition,
	before, after *Value,
) error {
	if definition == nil {
		return ErrNilDefinition
	}

	if after == nil {
		return ErrNilValue
	}

	changes, err := diffValues(before, after)
	if err != nil {
		return err
	}

	var changed []string
	if before != nil {
		changed = changedFields(changes)
	}

	eventType := audit.EventUpdated
	if before == nil {
		eventType = audit.EventCreated
	}

	return h.recordValue(ctx, tx, scope, definition, after, eventType, EventValueSet, changes, changed)
}

// AfterClearValue records a subject withdrawing their answer. The value is the
// one the clearing archived, which still carries the answer, so the entry's diff
// records the archive stamp and the answer withdrawn: what the subject had
// chosen, with no new value, because resolution now falls back to the default.
func (h *RecordingHooks) AfterClearValue(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	definition *Definition,
	value *Value,
) error {
	if definition == nil {
		return ErrNilDefinition
	}

	if value == nil {
		return ErrNilValue
	}

	live := *value
	live.ArchivedAt = nil

	changes, err := diffValues(&live, value)
	if err != nil {
		return err
	}

	changes[rawValueField] = audit.Change{Old: value.Raw}

	return h.recordValue(ctx, tx, scope, definition, value, audit.EventArchived, EventValueCleared, changes, nil)
}

// AfterDeleteValuesForSubject records an erasure: that it ran, how many answers
// it deleted, and what kind of subject they were. Zero is recorded too, because
// the erasure ran. It names no subject, for the reason
// waitlists.RecordingHooks.AfterWithdrawSignupsForSubject gives: this entry is
// about the erasure and survives it.
func (h *RecordingHooks) AfterDeleteValuesForSubject(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subject Subject,
	deleted int64,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeValue,
		EventType:    audit.EventDeleted,
		Metadata: map[string]string{
			metadataSubjectType: string(subject.Type),
			metadataDeleted:     strconv.FormatInt(deleted, 10),
		},
	}

	event := &webhooks.Event{
		EventType: EventValuesErased,
		Payload:   &ErasureEvent{SubjectType: subject.Type, Deleted: deleted},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// recordDefinition writes the entry and the event for a write to the catalog.
func (h *RecordingHooks) recordDefinition(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	definition *Definition,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
	changes map[string]audit.Change,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeDefinition,
		ResourceID:   definition.ID,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata:     map[string]string{metadataName: definition.Name},
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: definition.ID,
		Payload: &DefinitionEvent{
			DefinitionID: definition.ID,
			Name:         definition.Name,
			Changed:      changedFields(changes),
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// recordValue writes the entry and the event for a write to one subject's
// answer. The entry's SubjectID is the value's subject, so a Recorder filing by
// subject can; the metadata carries the kind of subject and never its ID.
func (h *RecordingHooks) recordValue(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	definition *Definition,
	value *Value,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
	changes map[string]audit.Change,
	changed []string,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeValue,
		ResourceID:   value.ID,
		SubjectID:    value.Subject.ID,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata: map[string]string{
			metadataDefinitionID: value.DefinitionID,
			metadataName:         definition.Name,
			metadataSubjectType:  string(value.Subject.Type),
		},
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: value.ID,
		Payload: &ValueEvent{
			ValueID:      value.ID,
			DefinitionID: value.DefinitionID,
			Name:         definition.Name,
			Subject:      value.Subject,
			Changed:      changed,
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// diffValues diffs two answers with the subject blanked on both sides.
//
// A value's subject never moves, so blanking it loses nothing from a revision;
// what it keeps out is the diff against nothing, which would otherwise record
// the subject's ID as an addition in an entry filed where the write ran, beyond
// the reach of the subject's erasure. Entry.SubjectID is how the entry names
// them instead.
func diffValues(before, after *Value) (map[string]audit.Change, error) {
	var blankedBefore *Value
	if before != nil {
		copied := *before
		copied.Subject = Subject{}
		blankedBefore = &copied
	}

	blankedAfter := *after
	blankedAfter.Subject = Subject{}

	changes, err := audit.Diff(blankedBefore, &blankedAfter)
	if err != nil {
		return nil, platformerrors.Wrap(err, "diffing the setting value")
	}

	return changes, nil
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
