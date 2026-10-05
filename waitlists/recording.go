package waitlists

import (
	"context"
	"maps"
	"slices"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The resource types this package's audit entries name. They are platform's
// vocabulary for platform's own tables, prefixed so a consumer's "list" is
// never mistaken for one of these.
const (
	// ResourceTypeList is what an audit entry about a List names.
	ResourceTypeList = "waitlists.list"
	// ResourceTypeSignup is what an audit entry about a Signup names.
	ResourceTypeSignup = "waitlists.signup"
)

// The events this package's writes emit, one per write that publishes. They
// are platform's names for platform's own writes, which is what makes them
// constants here rather than strings a consumer mints: a consumer names the
// nouns it owns, as webhooks documents with order.created, and this package
// names these.
//
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type
// left out of it is still published to the outbox and dispatched to nobody.
const (
	// EventListCreated says a list was opened.
	EventListCreated webhooks.EventType = "waitlists.list.created"
	// EventListUpdated says a list's details were rewritten; the payload names
	// which fields.
	EventListUpdated webhooks.EventType = "waitlists.list.updated"
	// EventListArchived says a list was retired.
	EventListArchived webhooks.EventType = "waitlists.list.archived"

	// EventSignupJoined says somebody joined a list, in the status the payload
	// carries: pending when the list confirms addresses, waiting otherwise.
	EventSignupJoined webhooks.EventType = "waitlists.signup.joined"
	// EventSignupNotesUpdated says the operator's note on a signup changed. It
	// is the one signup write that moves nobody.
	EventSignupNotesUpdated webhooks.EventType = "waitlists.signup.notes_updated"
	// EventSignupConfirmed says a pending signup confirmed its address and
	// entered the queue.
	EventSignupConfirmed webhooks.EventType = "waitlists.signup.confirmed"
	// EventSignupInvited says somebody was let in.
	EventSignupInvited webhooks.EventType = "waitlists.signup.invited"
	// EventSignupConverted says an invitation was taken up.
	EventSignupConverted webhooks.EventType = "waitlists.signup.converted"
	// EventSignupWithdrawn says somebody came off a list at their own request.
	// The payload names the signup by ID and digest and never by address.
	EventSignupWithdrawn webhooks.EventType = "waitlists.signup.withdrawn"
	// EventSignupArchived says a signup was retired administratively.
	EventSignupArchived webhooks.EventType = "waitlists.signup.archived"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with:
//
//	catalog, err := webhooks.Merge(
//		webhooks.Catalog{OrderCreated: {Description: "..."}},
//		waitlists.EventCatalog(),
//	)
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventListCreated:        {Description: "A waitlist was opened."},
		EventListUpdated:        {Description: "A waitlist's name, description or closing time changed."},
		EventListArchived:       {Description: "A waitlist was retired."},
		EventSignupJoined:       {Description: "Somebody joined a waitlist."},
		EventSignupNotesUpdated: {Description: "An operator's note on a waitlist signup changed."},
		EventSignupConfirmed:    {Description: "A pending waitlist signup confirmed its address and entered the queue."},
		EventSignupInvited:      {Description: "A waiting signup was invited in."},
		EventSignupConverted:    {Description: "An invited signup took up its invitation."},
		EventSignupWithdrawn:    {Description: "Somebody withdrew from a waitlist."},
		EventSignupArchived:     {Description: "A waitlist signup was retired by an operator."},
	}
}

// ListEvent is the payload of every list event.
//
// It carries the identifiers a subscriber needs to go and read the list, and
// for an update the names of the fields that moved, rather than the row: a
// subscriber told a list changed reads the list for what it is now, and a
// payload that carried the row would be a second copy of it in every
// subscriber's logs.
type ListEvent struct {
	_ struct{} `json:"-"`

	// ListID is the list the event is about.
	ListID string `json:"listID"`
	// Name is the list's name as the write left it, so a subscriber can say
	// which list without a read.
	Name string `json:"name"`

	// Changed names the fields an update moved, sorted, and is empty for every
	// other event. The timestamp every save stamps is left off: a subscriber
	// asking what the operator edited is told nothing by it.
	Changed []string `json:"changed,omitempty"`
}

// SignupEvent is the payload of every signup event.
//
// It names the signup by its ID, its subject and its contact digest, and never
// by the address. The address is the personal data a withdrawal exists to
// erase, and an event is copied into every subscriber's logs; a subscriber that
// needs to write to the person reads the signup while it still holds one.
type SignupEvent struct {
	_ struct{} `json:"-"`

	// Subject is whose signup it is, where the signup names one.
	Subject Subject `json:"subject"`
	// SignupID is the signup the event is about.
	SignupID string `json:"signupID"`
	// ListID is the list it is on.
	ListID string `json:"listID"`
	// ContactDigest identifies the address without disclosing it.
	ContactDigest string `json:"contactDigest"`
	// Status is where the signup stands after the write.
	Status Status `json:"status"`
	// PreviousStatus is where it stood before, on the events that moved it,
	// and empty on the ones that did not.
	PreviousStatus Status `json:"previousStatus,omitempty"`

	// Changed names the fields an update moved, sorted, and is empty for every
	// other event. See ListEvent.Changed for what is left off.
	Changed []string `json:"changed,omitempty"`
}

// The metadata keys an audit entry here carries. Strings rather than the
// observability keys above, because they are read back by whoever reads the
// log, not by a span.
const (
	metadataListID         = "listID"
	metadataStatus         = "status"
	metadataPreviousStatus = "previousStatus"
	metadataSubjectType    = "subjectType"
)

// signupText is what a signup's diff hashes before it is recorded: the
// operator's notes, which are text about a person. See RecordingHooks.
var signupText = audit.Redaction{Hash: []string{"notes"}}

// lastUpdatedAtField is the json name of the timestamp every update stamps,
// which a diff therefore always names and a changed-fields list should not.
const lastUpdatedAtField = "lastUpdatedAt"

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. Every write but one records an
// audit entry naming the row and emits the event above for it, both on the
// write's transaction, through the recording.Recorder it is built with. An
// erasure records nothing, for the reason AfterWithdrawSignupsForSubject gives.
//
// It implements Hooks outright rather than embedding NoopHooks, so a write
// added to Store later fails to compile here until somebody decides what it
// records. Embedded, the new write would compile and record nothing, which is
// the one failure an audit log cannot notice. That makes it the type a
// consumer embeds in turn: one that wants a single entry shaped differently
// overrides that method and inherits the rest.
//
// What it does not decide is where an entry is filed or who made it; both are
// the Recorder's, through its ScopeResolver and its principal extractor. A
// deployment that keeps its lists in tenancy.Global and wants a signup's entries
// filed under the signup's subject gives the Recorder a resolver that reads
// Entry.SubjectID, which every signup entry here sets. That is also the only
// way a signup's entries reach the subject: none of them names the subject in
// its metadata, for the reason recordSignup gives.
//
// A Signup's Contact is tagged `audit:"-"` and never reaches a diff. Its Notes
// do, hashed: an operator's note is text about a person, a withdrawal blanks it
// and waitlists/privacy's eraser withdraws, and a copy in the one table built
// not to forget is one neither can reach. The digest still says the note
// changed, and the field's name survives into the event's Changed list. That is
// this type's obligation rather than a deployment's policy, so it does not wait
// for an audit.WithRedaction; see audit.Redaction.Apply.
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

// AfterCreateList records a list being opened.
func (h *RecordingHooks) AfterCreateList(ctx context.Context, tx database.Tx, scope tenancy.Scope, list *List) error {
	if list == nil {
		return ErrNilList
	}

	return h.recordList(ctx, tx, scope, list, audit.EventCreated, EventListCreated, nil)
}

// AfterUpdateList records a list being rewritten. The audit entry carries the
// diff, old values and new; the event carries only the names of the fields that
// moved.
func (h *RecordingHooks) AfterUpdateList(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *List) error {
	if before == nil || after == nil {
		return ErrNilList
	}

	changes, err := audit.Diff(before, after)
	if err != nil {
		return platformerrors.Wrap(err, "diffing the updated waitlist")
	}

	return h.recordList(ctx, tx, scope, after, audit.EventUpdated, EventListUpdated, changes)
}

// AfterArchiveList records a list being retired.
func (h *RecordingHooks) AfterArchiveList(ctx context.Context, tx database.Tx, scope tenancy.Scope, list *List) error {
	if list == nil {
		return ErrNilList
	}

	return h.recordList(ctx, tx, scope, list, audit.EventArchived, EventListArchived, nil)
}

// AfterJoin records somebody joining a list, in the status they joined at.
func (h *RecordingHooks) AfterJoin(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error {
	if signup == nil {
		return ErrNilSignup
	}

	return h.recordSignup(ctx, tx, scope, signup, audit.EventCreated, EventSignupJoined, signup.Status, "", nil)
}

// AfterUpdateSignupNotes records the operator's note changing. The diff carries
// the note old and new as digests, never as written, for the reason the type's
// documentation gives.
func (h *RecordingHooks) AfterUpdateSignupNotes(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	before, after *Signup,
) error {
	if before == nil || after == nil {
		return ErrNilSignup
	}

	changes, err := audit.Diff(before, after)
	if err != nil {
		return platformerrors.Wrap(err, "diffing the updated waitlist signup")
	}

	if changes, err = signupText.Apply(changes); err != nil {
		return platformerrors.Wrap(err, "redacting the updated waitlist signup")
	}

	return h.recordSignup(ctx, tx, scope, after, audit.EventUpdated, EventSignupNotesUpdated, after.Status, "", changes)
}

// AfterConfirm records a pending signup entering the queue.
func (h *RecordingHooks) AfterConfirm(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error {
	if signup == nil {
		return ErrNilSignup
	}

	return h.recordSignup(ctx, tx, scope, signup, audit.EventUpdated, EventSignupConfirmed, signup.Status, StatusPending, nil)
}

// AfterInvite records somebody being let in.
func (h *RecordingHooks) AfterInvite(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error {
	if signup == nil {
		return ErrNilSignup
	}

	return h.recordSignup(ctx, tx, scope, signup, audit.EventUpdated, EventSignupInvited, signup.Status, StatusWaiting, nil)
}

// AfterConvert records an invitation being taken up.
func (h *RecordingHooks) AfterConvert(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error {
	if signup == nil {
		return ErrNilSignup
	}

	return h.recordSignup(ctx, tx, scope, signup, audit.EventUpdated, EventSignupConverted, signup.Status, StatusInvited, nil)
}

// AfterWithdraw records somebody coming off a list at their own request.
//
// The row is the one from before the blanking, so its Status is the one the
// signup was withdrawn from and its Subject still names the person; the status
// recorded is StatusWithdrawn, which is where the row now is. Its Contact is on
// the row too and goes nowhere: SignupEvent has no field for it, and the entry
// carries no diff.
func (h *RecordingHooks) AfterWithdraw(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error {
	if signup == nil {
		return ErrNilSignup
	}

	return h.recordSignup(ctx, tx, scope, signup, audit.EventUpdated, EventSignupWithdrawn, StatusWithdrawn, signup.Status, nil)
}

// AfterWithdrawSignupsForSubject records nothing, deliberately.
//
// SignupStore.WithdrawSignupsForSubject is an erasure's write, one table of the
// many a single erasure request reaches, and dataprivacy.Fulfiller records that
// request once: one audit entry naming it, with this table's count among the
// per-section counts in its metadata, and one dataprivacy.EventErasureFulfilled.
// An entry and an event here as well would be the request's fan-out across
// stores written down as a separate fact per store, none of which says which
// erasure it belonged to.
//
// A consumer that calls SignupStore.WithdrawSignupsForSubject outside an erasure
// has a withdrawal nothing else recorded, and embeds this type to override the
// method.
func (h *RecordingHooks) AfterWithdrawSignupsForSubject(context.Context, database.Tx, tenancy.Scope, Subject, int64) error {
	return nil
}

// AfterArchiveSignup records a signup being retired administratively.
func (h *RecordingHooks) AfterArchiveSignup(ctx context.Context, tx database.Tx, scope tenancy.Scope, signup *Signup) error {
	if signup == nil {
		return ErrNilSignup
	}

	return h.recordSignup(ctx, tx, scope, signup, audit.EventArchived, EventSignupArchived, signup.Status, "", nil)
}

// recordList writes the entry and the event for a write to the catalog. The
// entry names no subject: a list is an administrative row that belongs to
// nobody, so it is filed where the write ran.
func (h *RecordingHooks) recordList(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	list *List,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
	changes map[string]audit.Change,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeList,
		ResourceID:   list.ID,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata:     map[string]string{metadataListID: list.ID},
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: list.ID,
		Payload: &ListEvent{
			ListID:  list.ID,
			Name:    list.Name,
			Changed: changedFields(changes),
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// recordSignup writes the entry and the event for a write to one signup. The
// entry's SubjectID is the signup's subject, so a Recorder filing by subject
// can.
//
// The subject's ID is not copied into the metadata. Filed by subject, the entry
// is on the subject's own chain, which names them already and which
// audit.Erasure deletes whole. Filed where the write ran, an ID in the metadata
// would outlive the erasure that blanks it from the row, uncounted in what the
// subject is told is retained and missing from their export, because neither
// reads metadata. The signup's ID is the entry's resource, and after an erasure
// it leads to a row that names nobody.
func (h *RecordingHooks) recordSignup(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	signup *Signup,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
	status, previous Status,
	changes map[string]audit.Change,
) error {
	metadata := map[string]string{
		metadataListID: signup.ListID,
		metadataStatus: status.String(),
	}

	if previous != "" {
		metadata[metadataPreviousStatus] = previous.String()
	}

	if signup.Subject.ID != "" {
		metadata[metadataSubjectType] = string(signup.Subject.Type)
	}

	entry := &recording.Entry{
		ResourceType: ResourceTypeSignup,
		ResourceID:   signup.ID,
		SubjectID:    signup.Subject.ID,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata:     metadata,
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: signup.ID,
		Payload: &SignupEvent{
			SignupID:       signup.ID,
			ListID:         signup.ListID,
			Subject:        signup.Subject,
			ContactDigest:  signup.ContactDigest,
			Status:         status,
			PreviousStatus: previous,
			Changed:        changedFields(changes),
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
