package passkeys

import (
	"context"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ResourceTypeCredential is what an audit entry about a Credential names. It is
// platform's vocabulary for platform's own table, prefixed so a consumer's
// "credential" is never mistaken for one of these.
//
//nolint:gosec // G101: a resource type name; no credential is on the entry.
const ResourceTypeCredential = "passkeys.credential"

// The events this package's writes emit. They are platform's names for
// platform's own writes, which is what makes them constants here rather than
// strings a consumer mints.
//
// Both are credential events: they are recorded and published to the outbox,
// where a deployment's own consumers may read them, and they are offered to no
// webhook subscriber. [EventCatalog] leaves them out, and an event type the
// dispatcher's catalog does not know is published and dispatched to nobody —
// see webhooks.Emitter.Emit for the gate. A deployment that does want a third
// party told when somebody's sign-in methods change adds them to its catalog by
// name, which is a decision it then has to spell.
const (
	// EventPasskeyRegistered says a passkey was added to somebody's account.
	//nolint:gosec // G101: an event name; no credential is on the event.
	EventPasskeyRegistered webhooks.EventType = "passkeys.credential.registered"
	// EventPasskeyArchived says a passkey was revoked.
	//nolint:gosec // G101: an event name; no credential is on the event.
	EventPasskeyArchived webhooks.EventType = "passkeys.credential.archived"
)

// EventCatalog is the fragment of a dispatcher's catalog this package
// contributes, and it is empty on purpose.
//
// A change to how somebody signs in is the event an attacker who has just taken
// over an account most wants to be invisible, and a webhook endpoint is a third
// party: every subscriber the event reaches is one more place the account's
// sign-in history is copied to, for a payload nobody outside the deployment
// needs in order to act. So the default is the safe one, and a consumer who
// merges every package's catalog by habit makes no passkey event subscribable
// by doing so. It exists, and is called, so that habit has nothing to special-case.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{}
}

// CredentialEvent is the payload of EventPasskeyRegistered and
// EventPasskeyArchived.
//
// It names the passkey by row ID and its owner by opaque ID, with the name the
// person gave it so a notice can say which one. It carries neither the
// authenticator's credential ID nor its public key: neither is a secret, and
// neither tells a reader of the outbox anything the row ID does not.
type CredentialEvent struct {
	_ struct{} `json:"-"`

	// CredentialID is the row the event is about.
	CredentialID string `json:"credentialID"`
	// UserID is whose passkey it is.
	UserID string `json:"userID"`
	// FriendlyName is the name the person gave it.
	FriendlyName string `json:"friendlyName"`
}

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. A registration and a revocation
// each record an audit entry and emit the event above, both on the write's
// transaction, through the recording.Recorder it is built with. A refused login
// records nothing; see AfterFailedPasskeyLogin.
//
// It implements Hooks outright rather than embedding NoopHooks, so a write
// added to Hooks later fails to compile here until somebody decides what it
// records. That makes it the type a consumer embeds in turn: one that wants
// failed logins counted overrides AfterFailedPasskeyLogin and inherits the rest.
//
// An entry names its user through Entry.SubjectID and not in its metadata, so a
// Recorder filing by subject puts it on the user's chain, and the default files
// it where the write ran. It carries no metadata at all: the friendly name is a
// label the person chose, and an audit log is the wrong place to keep a copy of
// it once they rename or revoke the passkey.
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

// AfterRegisterPasskey records a passkey being added to somebody's account.
func (h *RecordingHooks) AfterRegisterPasskey(ctx context.Context, tx database.Tx, scope tenancy.Scope, credential *Credential) error {
	if credential == nil {
		return ErrNilCredential
	}

	return h.recordCredential(ctx, tx, scope, credential, audit.EventCreated, EventPasskeyRegistered)
}

// AfterArchivePasskey records a passkey being revoked. The row is the one the
// archive left, so it still says whose passkey it was and what it was called.
func (h *RecordingHooks) AfterArchivePasskey(ctx context.Context, tx database.Tx, scope tenancy.Scope, credential *Credential) error {
	if credential == nil {
		return ErrNilCredential
	}

	return h.recordCredential(ctx, tx, scope, credential, audit.EventArchived, EventPasskeyArchived)
}

// AfterFailedPasskeyLogin records nothing.
//
// A refused login is not a write, and whoever made it proved nobody, so an
// entry for it would be one any caller on the internet can append, unattributed,
// as often as they like: the audit log turned into a table anonymous traffic
// writes to, on a transaction opened for no other reason. What a refusal is for
// — a lockout counter, an alert on a cloned key — is a policy, and its storage
// is the deployment's. A deployment that wants it embeds this type and
// overrides this method, which is the seam the hook was declared for.
//
// It is the question signin.Hooks.AfterFailedSignIn asks of a password, a code
// or a link, and the answer here is the answer there: a refused sign-in records
// nothing either, for the same reasons in the same words.
func (h *RecordingHooks) AfterFailedPasskeyLogin(context.Context, database.Tx, tenancy.Scope, *FailedLogin) error {
	return nil
}

// recordCredential writes the entry and the event for a write to one passkey.
func (h *RecordingHooks) recordCredential(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	credential *Credential,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeCredential,
		ResourceID:   credential.ID,
		SubjectID:    credential.BelongsToUser,
		EventType:    auditEventType,
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: credential.ID,
		Payload: &CredentialEvent{
			CredentialID: credential.ID,
			UserID:       credential.BelongsToUser,
			FriendlyName: credential.FriendlyName,
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}
