package oauth2clients

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

// ResourceTypeClient is what an audit entry about a Client names. It is
// platform's vocabulary for platform's own table, prefixed so a consumer's
// "client" is never mistaken for one of these.
const ResourceTypeClient = "oauth2clients.client"

// The events this package's writes emit, one per write. They are platform's
// names for platform's own writes, which is what makes them constants here
// rather than strings a consumer mints.
//
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type left
// out of it is still published to the outbox and dispatched to nobody.
const (
	// EventClientCreated says a client was registered.
	EventClientCreated webhooks.EventType = "oauth2clients.client.created"
	// EventClientUpdated says a registration was revised; the payload names
	// which fields.
	EventClientUpdated webhooks.EventType = "oauth2clients.client.updated"
	// EventClientArchived says a registration was withdrawn.
	EventClientArchived webhooks.EventType = "oauth2clients.client.archived"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with:
//
//	maps.Copy(catalog, oauth2clients.EventCatalog())
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
//
// Unlike passkeys' these are subscribable by default. A registration is a
// credential, but these events say that an application was admitted to the
// registry and under what name, which is what an integration directory or an
// operator's notice is built from; the secret is on none of them, and no event
// here can carry it, because a *Client is all a hook is ever handed.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventClientCreated:  {Description: "An OAuth2 client was registered."},
		EventClientUpdated:  {Description: "An OAuth2 client's name, description, redirect URIs or scopes changed."},
		EventClientArchived: {Description: "An OAuth2 client's registration was withdrawn."},
	}
}

// ClientEvent is the payload of every client event.
//
// It carries the identifiers a subscriber needs to go and read the
// registration, the name a notice would show, and for an update the names of
// the fields that moved, rather than the row: a subscriber told a client
// changed reads the client for what it is now.
type ClientEvent struct {
	_ struct{} `json:"-"`

	// ID is the row the event is about.
	ID string `json:"id"`
	// ClientID is the identifier the client sends at /authorize and /token.
	ClientID string `json:"clientID"`
	// OwnerID is the person who owns the registration, and empty for one
	// nobody owns.
	OwnerID string `json:"ownerID,omitempty"`
	// Name is the registration's name as the write left it.
	Name string `json:"name"`

	// Changed names the fields an update moved, sorted, and is empty for every
	// other event. The timestamp every revision stamps is left off: a subscriber
	// asking what the operator edited is told nothing by it.
	Changed []string `json:"changed,omitempty"`
}

// metadataClientID is the metadata key an entry carries the protocol
// identifier under, so a reader holding a client_id from a token request can
// find the registration's history without first resolving the row.
const metadataClientID = "clientID"

// lastUpdatedAtField is the json name of the timestamp every revision stamps,
// which a diff therefore always names and a changed-fields list should not.
const lastUpdatedAtField = "lastUpdatedAt"

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. Every operation records an audit
// entry naming the registration and emits the event above for it, both on the
// operation's transaction, through the recording.Recorder it is built with.
//
// It implements Hooks outright rather than embedding NoopHooks, so a write
// added to Hooks later fails to compile here until somebody decides what it
// records. That makes it the type a consumer embeds in turn: one that wants a
// single entry shaped differently overrides that method and inherits the rest.
//
// The secret reaches none of it. A hook is handed a *Client, never an
// IssuedClient, so the plaintext is not here to leak; the digest is on the row
// and tagged json:"-", which audit.Diff skips, so it is in no entry's Changes
// either. An entry names the registration's owner through Entry.SubjectID and
// not in its metadata, so a Recorder filing by subject can, and an administered
// registration, which nobody owns, names none.
type RecordingHooks struct {
	recorder *recording.Recorder
}

var _ Hooks = (*RecordingHooks)(nil)

// NewRecordingHooks builds the Hooks that record every operation through
// recorder.
func NewRecordingHooks(recorder *recording.Recorder) (*RecordingHooks, error) {
	if recorder == nil {
		return nil, ErrNilRecorder
	}

	return &RecordingHooks{recorder: recorder}, nil
}

// AfterCreateClient records a client being registered.
func (h *RecordingHooks) AfterCreateClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, client *Client) error {
	if client == nil {
		return ErrNilClient
	}

	return h.recordClient(ctx, tx, scope, client, audit.EventCreated, EventClientCreated, nil)
}

// AfterUpdateClient records a registration being revised. The audit entry
// carries the diff, old values and new; the event carries only the names of
// the fields that moved.
//
// The secret's digest is never among them. It is tagged json:"-", which
// audit.Diff skips, and it cannot move here anyway: a revision takes an
// UpdateInput, which has no field for it, so rotating a secret is not a
// revision and is not recorded as one.
func (h *RecordingHooks) AfterUpdateClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Client) error {
	if before == nil || after == nil {
		return ErrNilClient
	}

	changes, err := audit.Diff(before, after)
	if err != nil {
		return platformerrors.Wrap(err, "diffing the updated oauth2 client")
	}

	return h.recordClient(ctx, tx, scope, after, audit.EventUpdated, EventClientUpdated, changes)
}

// AfterArchiveClient records a registration being withdrawn.
//
// The row is the one the archive left, which matters here more than anywhere:
// every read of the registry but client_id resolution filters archived rows
// out, so once this transaction commits the hook's argument is the last value
// that says what the withdrawn registration was called and whose it was,
// without going around the store.
func (h *RecordingHooks) AfterArchiveClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, client *Client) error {
	if client == nil {
		return ErrNilClient
	}

	return h.recordClient(ctx, tx, scope, client, audit.EventArchived, EventClientArchived, nil)
}

// recordClient writes the entry and the event for a write to one registration.
func (h *RecordingHooks) recordClient(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	client *Client,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
	changes map[string]audit.Change,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeClient,
		ResourceID:   client.ID,
		SubjectID:    client.BelongsToUser,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata:     map[string]string{metadataClientID: client.ClientID},
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: client.ID,
		Payload: &ClientEvent{
			ID:       client.ID,
			ClientID: client.ClientID,
			OwnerID:  client.BelongsToUser,
			Name:     client.Name,
			Changed:  changedFields(changes),
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// changedFields names the fields a diff says moved, sorted, without the
// timestamp every revision stamps. It is nil for a nil diff, so an event for a
// write that is not an update carries no changed list at all.
func changedFields(changes map[string]audit.Change) []string {
	if len(changes) == 0 {
		return nil
	}

	fields := slices.Sorted(maps.Keys(changes))

	return slices.DeleteFunc(fields, func(field string) bool { return field == lastUpdatedAtField })
}
