/*
Package recordinghooks is the webhooks.Hooks a deployment that keeps an audit
log and publishes events installs on its webhooks store: every endpoint and
subscription write records an audit entry naming the row and emits one of the
events webhooks.EventCatalog describes, both on the write's transaction,
through a recording.Recorder.

It is a package of its own rather than a type in webhooks, which is where every
other store's RecordingHooks lives, because recording imports webhooks for its
Emitter: the one type built over a Recorder cannot sit in the package the
Recorder is built over. The names, the catalog fragment and the payloads need
nothing from recording and are in webhooks, beside the rows.

# Wiring it without a cycle

The events this package emits fan out through a webhooks.Dispatcher, and that
dispatcher reads its subscribers off a webhooks.Store. The store these hooks are
installed on cannot be that one, because WithHooks is a construction option
and the hooks need a Recorder, which needs an Emitter, which needs the
dispatcher. So a deployment builds two stores over the same database and
prefix: one with no hooks under the dispatcher its Emitter fans out through,
and one with these hooks under everything that writes endpoints and
subscriptions.

	fanout, _ := webhooks.NewSQLStore(client)
	fanoutDispatcher, _ := webhooks.NewDispatcher(fanout, client.Reader(), webhooks.WithCatalog(catalog))
	emitter, _ := webhooks.NewEmitter(outboxWriter, fanoutDispatcher, topic)
	recorder, _ := recording.New(auditRecorder, emitter, principals)
	hooks, _ := recordinghooks.NewRecordingHooks(recorder)

	store, _ := webhooks.NewSQLStore(client, webhooks.WithHooks(hooks))
	dispatcher, _ := webhooks.NewDispatcher(store, client.Reader(), webhooks.WithCatalog(catalog))

The hookless store loses nothing by it. Dispatch reads subscribers and writes
the queue, and neither has a hook; the endpoint writes that do go through the
second store, whichever surface makes them.

# Its own events, and its own endpoints

An endpoint may subscribe to the events about endpoints, itself included. It
is told about its own creation, because the hook dispatches once the row and its
subscriptions exist, and not about its own archival, because the hook
dispatches once the archive has taken effect and fan-out skips archived
endpoints. Neither loops: deliveries are written by Dispatch and drained by the
Worker, and neither writes an endpoint or a subscription, which are the only
writes with hooks.
*/
package recordinghooks

import (
	"context"
	"maps"
	"slices"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/recording"
	"github.com/primandproper/platform-go/v14/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The metadata keys an audit entry here carries. They are read back by whoever
// reads the log.
const (
	metadataEndpointID = "endpointID"
	metadataEventType  = "eventType"
	metadataRotated    = "rotated"
)

// rotatedSecret is the value of metadataRotated on a rotation's entry: the
// name of what changed, and not the change.
const rotatedSecret = "secret"

// The json names of the fields changedFields treats specially. lastUpdatedAt
// is stamped by every save, and subscriptions is a slice of rows each save
// restamps, so a diff names both on every re-registration whether or not
// anything a subscriber cares about moved.
const (
	lastUpdatedAtField = "lastUpdatedAt"
	subscriptionsField = "subscriptions"
)

// RecordingHooks records every endpoint and subscription write through a
// recording.Recorder. See the package documentation for how to wire it.
//
// It implements webhooks.Hooks outright rather than embedding NoopHooks, so a
// write added to the Store later fails to compile here until somebody decides
// what it records. That makes it the type a consumer embeds in turn: one that
// wants a single entry shaped differently overrides that method and inherits
// the rest.
//
// What it does not decide is where an entry is filed or who made it; both are
// the Recorder's. Nor does it redact anything beyond what the types already
// keep out: an Endpoint's Headers are tagged `audit:"-"` and its Secret
// `json:"-"`, so neither reaches a diff, and neither payload has a field for
// them.
type RecordingHooks struct {
	recorder *recording.Recorder
}

var _ webhooks.Hooks = (*RecordingHooks)(nil)

// NewRecordingHooks builds the Hooks that record every write through recorder.
func NewRecordingHooks(recorder *recording.Recorder) (*RecordingHooks, error) {
	if recorder == nil {
		return nil, webhooks.ErrNilRecorder
	}

	return &RecordingHooks{recorder: recorder}, nil
}

// AfterSaveEndpoint records a registration: a creation when before is nil, and
// an update otherwise, whose entry carries the diff and whose event names the
// fields that moved.
//
// A re-registration that revives an archived endpoint is an update, because
// the store hands it the archived row as before. The diff then names
// archivedAt, and so does the event.
func (h *RecordingHooks) AfterSaveEndpoint(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	before, after *webhooks.Endpoint,
) error {
	if after == nil {
		return webhooks.ErrNilEndpoint
	}

	if before == nil {
		return h.recordEndpoint(ctx, tx, scope, after.ID, after.URL, audit.EventCreated, webhooks.EventEndpointCreated, nil, nil)
	}

	changes, err := audit.Diff(before, after)
	if err != nil {
		return platformerrors.Wrap(err, "diffing the saved webhook endpoint")
	}

	return h.recordEndpoint(
		ctx, tx, scope, after.ID, after.URL,
		audit.EventUpdated, webhooks.EventEndpointUpdated,
		changes, changedFields(before, after, changes),
	)
}

// AfterArchiveEndpoint records an endpoint being retired.
//
// The event it emits is not delivered to the endpoint it is about, whatever
// that endpoint subscribes to: this runs after the archive, on its transaction,
// and fan-out reads that transaction and skips archived endpoints.
func (h *RecordingHooks) AfterArchiveEndpoint(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	endpoint *webhooks.Endpoint,
) error {
	if endpoint == nil {
		return webhooks.ErrNilEndpoint
	}

	return h.recordEndpoint(ctx, tx, scope, endpoint.ID, endpoint.URL, audit.EventArchived, webhooks.EventEndpointArchived, nil, nil)
}

// AfterRotateSecret records that an endpoint's signing key changed. Who changed
// it is the entry's actor, which the Recorder reads off the context. Neither
// the entry nor the event names a key, old or new; the hook is handed none to
// name.
func (h *RecordingHooks) AfterRotateSecret(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	endpointID string,
) error {
	entry := &recording.Entry{
		ResourceType: webhooks.ResourceTypeEndpoint,
		ResourceID:   endpointID,
		EventType:    audit.EventUpdated,
		Metadata: map[string]string{
			metadataEndpointID: endpointID,
			metadataRotated:    rotatedSecret,
		},
	}

	event := &webhooks.Event{
		EventType:   webhooks.EventEndpointSecretRotated,
		OrderingKey: endpointID,
		Payload:     &webhooks.EndpointEvent{EndpointID: endpointID},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// AfterAddSubscription records a subscription as a creation whether the store
// created the row, revived an archived one, or found it live. Those are three
// states of a table and one fact to a subscriber: the endpoint receives this
// event type now.
func (h *RecordingHooks) AfterAddSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	_, after *webhooks.Subscription,
) error {
	if after == nil {
		return webhooks.ErrNilSubscription
	}

	return h.recordSubscription(ctx, tx, scope, after, audit.EventCreated, webhooks.EventSubscriptionCreated)
}

// AfterArchiveSubscription records an endpoint unsubscribing from an event
// type.
func (h *RecordingHooks) AfterArchiveSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subscription *webhooks.Subscription,
) error {
	if subscription == nil {
		return webhooks.ErrNilSubscription
	}

	return h.recordSubscription(ctx, tx, scope, subscription, audit.EventArchived, webhooks.EventSubscriptionArchived)
}

// recordEndpoint writes the entry and the event for a write to one endpoint.
// The event is ordered by the endpoint's ID, so an endpoint's update can never
// overtake its creation, and nor can a subscription's.
func (h *RecordingHooks) recordEndpoint(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	endpointID, url string,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
	changes map[string]audit.Change,
	changed []string,
) error {
	entry := &recording.Entry{
		ResourceType: webhooks.ResourceTypeEndpoint,
		ResourceID:   endpointID,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata:     map[string]string{metadataEndpointID: endpointID},
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: endpointID,
		Payload: &webhooks.EndpointEvent{
			EndpointID: endpointID,
			URL:        url,
			Changed:    changed,
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// recordSubscription writes the entry and the event for a write to one
// subscription. It is ordered by its endpoint's ID rather than its own, for the
// reason recordEndpoint gives.
func (h *RecordingHooks) recordSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subscription *webhooks.Subscription,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
) error {
	entry := &recording.Entry{
		ResourceType: webhooks.ResourceTypeSubscription,
		ResourceID:   subscription.ID,
		EventType:    auditEventType,
		Metadata: map[string]string{
			metadataEndpointID: subscription.EndpointID,
			metadataEventType:  subscription.EventType.String(),
		},
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: subscription.EndpointID,
		Payload: &webhooks.SubscriptionEvent{
			SubscriptionID: subscription.ID,
			EndpointID:     subscription.EndpointID,
			EventType:      subscription.EventType,
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// changedFields names the fields a re-registration moved, sorted, for the
// event. The audit entry keeps the whole diff; this is what a subscriber asking
// "what did they edit" is told.
//
// lastUpdatedAt is left off, since every save stamps it. subscriptions is
// named only if the set of event types moved: each save restamps the live
// rows, so the diff names the slice on every re-registration, and a subscriber
// told the subscriptions changed when the same events are subscribed to was
// told something false.
func changedFields(before, after *webhooks.Endpoint, changes map[string]audit.Change) []string {
	fields := slices.DeleteFunc(slices.Sorted(maps.Keys(changes)), func(field string) bool {
		return field == lastUpdatedAtField || field == subscriptionsField
	})

	if !slices.Equal(sortedEventTypes(before), sortedEventTypes(after)) {
		fields = append(fields, subscriptionsField)
		slices.Sort(fields)
	}

	if len(fields) == 0 {
		return nil
	}

	return fields
}

// sortedEventTypes is the endpoint's live event types in a stable order, so
// two saves naming the same set in a different order compare equal.
func sortedEventTypes(endpoint *webhooks.Endpoint) []webhooks.EventType {
	types := endpoint.EventTypes()
	slices.Sort(types)

	return types
}
