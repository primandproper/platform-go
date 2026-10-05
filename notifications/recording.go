package notifications

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
// vocabulary for platform's own tables, prefixed so a consumer's "device" is
// never mistaken for one of these.
const (
	// ResourceTypeNotification is what an audit entry about a Notification names.
	ResourceTypeNotification = "notifications.notification"
	// ResourceTypeDevice is what an audit entry about a Device names.
	ResourceTypeDevice = "notifications.device"
)

// The events this package's writes emit, one per write that publishes. They
// are platform's names for platform's own writes, which is what makes them
// constants here rather than strings a consumer mints.
//
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type
// left out of it is still published to the outbox and dispatched to nobody.
//
// The two mark-read writes have no event, because RecordingHooks records
// nothing for them; its documentation says why.
const (
	// EventNotificationCreated says somebody was told something.
	EventNotificationCreated webhooks.EventType = "notifications.notification.created"
	// EventNotificationArchived says a notification was dismissed.
	EventNotificationArchived webhooks.EventType = "notifications.notification.archived"

	// EventDeviceRegistered says a handset was registered for the first time in
	// the scope.
	EventDeviceRegistered webhooks.EventType = "notifications.device.registered"
	// EventDeviceReregistered says a handset already registered in the scope
	// announced itself again. The payload names the principal it was registered
	// to before, which differs from the one it is registered to now when the
	// handset changed hands.
	EventDeviceReregistered webhooks.EventType = "notifications.device.reregistered"
	// EventDeviceRevoked says a handset's registration was removed.
	EventDeviceRevoked webhooks.EventType = "notifications.device.revoked"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with:
//
//	catalog := webhooks.Catalog{OrderCreated: {Description: "..."}}
//	maps.Copy(catalog, notifications.EventCatalog())
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventNotificationCreated:  {Description: "A notification was filed in somebody's inbox."},
		EventNotificationArchived: {Description: "A notification was dismissed."},
		EventDeviceRegistered:     {Description: "A handset was registered for push notifications."},
		EventDeviceReregistered:   {Description: "A registered handset announced itself again, possibly under a new owner."},
		EventDeviceRevoked:        {Description: "A handset's push registration was removed."},
	}
}

// NotificationEvent is the payload of every notification event.
//
// It carries the identifiers a subscriber needs to go and read the
// notification, and its topic so a subscriber can route without a read, rather
// than the row: the title and body are what somebody was told, and a payload
// that carried them would be a second copy of it in every subscriber's logs.
type NotificationEvent struct {
	_ struct{} `json:"-"`

	// NotificationID is the notification the event is about.
	NotificationID string `json:"notificationID"`
	// Principal is whose inbox it is in.
	Principal string `json:"principal"`
	// Topic is the application's category for it.
	Topic string `json:"topic"`
}

// DeviceEvent is the payload of every device event.
//
// It names the registration by ID and platform and never by token. A token is
// a credential: whoever holds one can address a push to that handset through a
// provider account, and an event is copied into every subscriber's logs. A
// subscriber that needs to push goes through the registry, as
// notifications/push does.
type DeviceEvent struct {
	_ struct{} `json:"-"`

	// DeviceID is the registration the event is about.
	DeviceID string `json:"deviceID"`
	// Principal is whose handset it is after the write, or was, for a
	// revocation.
	Principal string `json:"principal"`
	// PreviousPrincipal is whose handset it was before a re-registration, and
	// empty on every other event. It differs from Principal when the handset
	// changed hands.
	PreviousPrincipal string `json:"previousPrincipal,omitempty"`
	// Platform is which provider the handset is addressed through.
	Platform Platform `json:"platform"`

	// Changed names the fields a re-registration moved, sorted, and is empty
	// for every other event. lastSeenAt is left off: every re-registration
	// stamps it, so a subscriber asking what changed is told nothing by it.
	Changed []string `json:"changed,omitempty"`
}

// The metadata keys an audit entry here carries.
const (
	metadataTopic    = "topic"
	metadataPlatform = "platform"
)

// lastSeenAtField is the json name of the timestamp every re-registration
// stamps, which a diff therefore always names and a changed-fields list should
// not.
const lastSeenAtField = "lastSeenAt"

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. Every write it records gets an
// audit entry naming the row and the event above for it, both on the write's
// transaction, through the recording.Recorder it is built with. Every entry
// about a row sets its SubjectID to the row's principal, so a Recorder filing
// by subject can.
//
// Two writes record nothing, by design: MarkNotificationRead and
// MarkAllNotificationsRead. An entry per read buries the entries that matter
// under an inbox being opened, and ReadAt on the row already says when it was
// read. A deployment that must audit reads does so as audit documents, with an
// audit.EventAccessed entry on a table of its own, and overrides those two
// methods to write it.
//
// The two erasures record nothing either, for the reason
// AfterDeleteNotificationsForPrincipal gives.
//
// It implements Hooks outright rather than embedding NoopHooks, so a write
// added to Store later fails to compile here until somebody decides what it
// records — the four no-ops above are that decision, made. That makes it the
// type a consumer embeds in turn: one that wants a single entry shaped
// differently overrides that method and inherits the rest.
//
// What it does not decide is where an entry is filed or who made it; both are
// the Recorder's, through its ScopeResolver and its principal extractor.
//
// Nor does it redact beyond the credential. A Device's Token is tagged
// `audit:"-"` and never reaches a diff, and no payload has a field for it.
// A re-registration's diff does name both the principal the handset left and
// the one it joined, because that is the fact the entry exists to record.
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

// AfterCreateNotification records somebody being told something.
func (h *RecordingHooks) AfterCreateNotification(ctx context.Context, tx database.Tx, scope tenancy.Scope, notification *Notification) error {
	if notification == nil {
		return ErrNilNotification
	}

	return h.recordNotification(ctx, tx, scope, notification, audit.EventCreated, EventNotificationCreated)
}

// AfterMarkNotificationRead records nothing. See the type's documentation.
func (*RecordingHooks) AfterMarkNotificationRead(context.Context, database.Tx, tenancy.Scope, *Notification, *Notification) error {
	return nil
}

// AfterMarkAllNotificationsRead records nothing. See the type's documentation.
func (*RecordingHooks) AfterMarkAllNotificationsRead(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

// AfterArchiveNotification records a notification being dismissed.
func (h *RecordingHooks) AfterArchiveNotification(ctx context.Context, tx database.Tx, scope tenancy.Scope, notification *Notification) error {
	if notification == nil {
		return ErrNilNotification
	}

	return h.recordNotification(ctx, tx, scope, notification, audit.EventArchived, EventNotificationArchived)
}

// AfterDeleteNotificationsForPrincipal records nothing, deliberately.
//
// Store.DeleteNotificationsForPrincipal is an erasure's write, one table of the many a
// single erasure request reaches, and dataprivacy.Fulfiller records that request
// once: one audit entry naming it, with this table's count among the
// per-section counts in its metadata, and one dataprivacy.EventErasureFulfilled.
// An entry and an event here as well would be the request's fan-out across
// stores written down as a separate fact per store, none of which says which
// erasure it belonged to.
//
// A consumer that calls Store.DeleteNotificationsForPrincipal outside an erasure has a
// deletion nothing else recorded, and embeds this type to override the method.
func (*RecordingHooks) AfterDeleteNotificationsForPrincipal(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

// AfterRegisterDevice records a handset being registered. A first registration
// is a create; a re-registration is an update carrying the diff, because a
// principal that moved is a handset that changed hands and the diff is what
// says so.
func (h *RecordingHooks) AfterRegisterDevice(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Device) error {
	if after == nil {
		return ErrNilDevice
	}

	if before == nil {
		return h.recordDevice(ctx, tx, scope, after, audit.EventCreated, EventDeviceRegistered, "", nil)
	}

	changes, err := audit.Diff(before, after)
	if err != nil {
		return platformerrors.Wrap(err, "diffing the re-registered device")
	}

	return h.recordDevice(ctx, tx, scope, after, audit.EventUpdated, EventDeviceReregistered, before.Principal, changes)
}

// AfterRevokeDevice records a handset's registration being removed. The row is
// the one from before the delete, which is what lets the entry say whose
// handset it was: revocation deletes, so there is nothing to read afterwards.
func (h *RecordingHooks) AfterRevokeDevice(ctx context.Context, tx database.Tx, scope tenancy.Scope, device *Device) error {
	if device == nil {
		return ErrNilDevice
	}

	return h.recordDevice(ctx, tx, scope, device, audit.EventDeleted, EventDeviceRevoked, "", nil)
}

// AfterDeleteDevicesForPrincipal records nothing, deliberately.
//
// Store.DeleteDevicesForPrincipal is an erasure's write, one table of the many a
// single erasure request reaches, and dataprivacy.Fulfiller records that request
// once: one audit entry naming it, with this table's count among the
// per-section counts in its metadata, and one dataprivacy.EventErasureFulfilled.
// An entry and an event here as well would be the request's fan-out across
// stores written down as a separate fact per store, none of which says which
// erasure it belonged to.
//
// A consumer that calls Store.DeleteDevicesForPrincipal outside an erasure has a
// deletion nothing else recorded, and embeds this type to override the method.
func (*RecordingHooks) AfterDeleteDevicesForPrincipal(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

// recordNotification writes the entry and the event for a write to one
// notification. The principal is the entry's SubjectID and is not copied into
// the metadata, for the reason recording.Entry's SubjectID gives.
func (h *RecordingHooks) recordNotification(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	notification *Notification,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeNotification,
		ResourceID:   notification.ID,
		SubjectID:    notification.Principal,
		EventType:    auditEventType,
		Metadata:     map[string]string{metadataTopic: notification.Topic},
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: notification.ID,
		Payload: &NotificationEvent{
			NotificationID: notification.ID,
			Principal:      notification.Principal,
			Topic:          notification.Topic,
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// recordDevice writes the entry and the event for a write to one registration.
// The principal is the entry's SubjectID, as recordNotification's is.
func (h *RecordingHooks) recordDevice(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	device *Device,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
	previousPrincipal string,
	changes map[string]audit.Change,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeDevice,
		ResourceID:   device.ID,
		SubjectID:    device.Principal,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata:     map[string]string{metadataPlatform: string(device.Platform)},
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: device.ID,
		Payload: &DeviceEvent{
			DeviceID:          device.ID,
			Principal:         device.Principal,
			PreviousPrincipal: previousPrincipal,
			Platform:          device.Platform,
			Changed:           changedFields(changes),
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// changedFields names the fields a diff says moved, sorted, without the
// timestamp every re-registration stamps. It is nil for a nil diff, so an event
// for a write that is not an update carries no changed list at all.
func changedFields(changes map[string]audit.Change) []string {
	if len(changes) == 0 {
		return nil
	}

	fields := slices.Sorted(maps.Keys(changes))

	return slices.DeleteFunc(fields, func(field string) bool { return field == lastSeenAtField })
}
