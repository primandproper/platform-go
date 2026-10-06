package passwordreset

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ResourceTypeToken is what an audit entry about a reset token names. It is
// platform's vocabulary for platform's own table, prefixed so a consumer's
// "token" is never mistaken for this one.
const ResourceTypeToken = "passwordreset.token"

// The events this package's writes emit, one per write that publishes. They
// are platform's names for platform's own writes, so they are constants here
// rather than strings a consumer mints.
//
// A subscriber may receive one only if the dispatcher's catalog knows it and
// does not mark it Internal. [EventCatalog] is the fragment to merge into that
// catalog, and it marks both Internal: neither carries the secret, but each
// says a reset is in flight, or has just landed, for a named user, which is a
// live feed of an account's takeover to whoever registered an endpoint on it.
// Both are still published to the outbox for a deployment's own consumers. One
// that does want them delivered clears Internal on its own merged copy.
const (
	// EventTokenIssued says a reset was asked for and a link minted for it. The
	// payload names the token and the principal, and never carries the secret.
	EventTokenIssued webhooks.EventType = "passwordreset.token.issued"
	// EventTokenRedeemed says a link was spent.
	EventTokenRedeemed webhooks.EventType = "passwordreset.token.redeemed"
)

// EventCatalog is every event this package emits, described and marked
// Internal as the constants' block says, for a consumer to merge into the
// catalog its dispatcher is built with:
//
//	catalog, err := webhooks.Merge(
//	    webhooks.Catalog{OrderCreated: {Description: "..."}},
//	    passwordreset.EventCatalog(),
//	)
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventTokenIssued:   {Description: "A password reset was asked for, and a link minted for it.", Internal: true},
		EventTokenRedeemed: {Description: "A password reset link was spent.", Internal: true},
	}
}

// TokenEvent is the payload of EventTokenIssued and EventTokenRedeemed.
//
// It is the Token's identifiers and deadlines, which is everything a hook is
// handed: there is no secret on it because there is none to put there. The
// secret goes to the caller of Store.Issue and nowhere else, and Service mails it
// through its Mailer on the way out. A subscriber is told a reset happened, not
// handed the means of completing one, and an event copied into every
// subscriber's logs is the last place a live reset link should be.
type TokenEvent struct {
	_ struct{} `json:"-"`

	// IssuedAt is when the reset was asked for.
	IssuedAt time.Time `json:"issuedAt"`
	// ExpiresAt is the link's deadline.
	ExpiresAt time.Time `json:"expiresAt"`
	// RedeemedAt is when the link was spent, on EventTokenRedeemed, and nil on
	// EventTokenIssued.
	RedeemedAt *time.Time `json:"redeemedAt,omitempty"`
	// TokenID is the row the event is about. It cannot be exchanged for a
	// password change.
	TokenID string `json:"tokenID"`
	// UserID is the principal the token resets.
	UserID string `json:"userID"`
}

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. An issuance and a redemption each
// record an audit entry and emit the event above for it, both on the write's
// transaction, through the recording.Recorder it is built with. A revocation
// and an erasure record nothing, for the reasons AfterRevokeForUser and
// AfterDeleteForUser give.
//
// The entries matter more here than for most tables: the sweeper deletes every
// row at its expiry, so "was a link issued before the takeover, and was it
// used?" is answered by the log or by nothing.
//
// It implements Hooks outright rather than embedding NoopHooks, so a write
// added to Store later fails to compile here until somebody decides what it
// records. Embedded, the new write would compile and record nothing, which is
// the one failure an audit log cannot notice. That makes it the type a
// consumer embeds in turn: one that wants a single entry shaped differently
// overrides that method and inherits the rest.
//
// Where an entry is filed and who made it are the Recorder's, through its
// ScopeResolver and its principal extractor. Every token entry sets
// Entry.SubjectID to the token's principal, so a Recorder filing by subject puts
// a person's resets on their own chain.
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

// AfterIssue records that a reset was asked for: the entry names the token, and
// the event names the token, its principal and its deadline. Neither carries
// the secret; see TokenEvent.
func (h *RecordingHooks) AfterIssue(ctx context.Context, tx database.Tx, scope tenancy.Scope, token *Token) error {
	if token == nil {
		return ErrNilToken
	}

	return h.recordToken(ctx, tx, scope, token, audit.EventCreated, EventTokenIssued)
}

// AfterConsume records a link being spent.
//
// The entry carries no diff. Consume hands no before row, and the one field
// the redemption moved is RedeemedAt, which the entry's own timestamp already
// says; a diff of it would be the entry repeating itself.
func (h *RecordingHooks) AfterConsume(ctx context.Context, tx database.Tx, scope tenancy.Scope, token *Token) error {
	if token == nil {
		return ErrNilToken
	}

	return h.recordToken(ctx, tx, scope, token, audit.EventUpdated, EventTokenRedeemed)
}

// AfterRevokeForUser records nothing, deliberately.
//
// Service.Complete is the one caller of RevokeForUser here, and it calls it on
// the same transaction immediately after a Consume this type has already
// recorded: a completed reset kills the principal's other outstanding links.
// An entry for the revocation would be the redemption's entry again, under a
// second name, and an event would tell every subscriber about one reset twice.
//
// A consumer that calls Store.RevokeForUser on its own — an operator revoking
// somebody's links with no reset behind it — has a revocation nothing else
// recorded, and embeds this type to override the method.
func (h *RecordingHooks) AfterRevokeForUser(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

// AfterDeleteForUser records nothing, deliberately.
//
// Store.DeleteForUser is an erasure's write, one table of the many a single
// erasure request reaches, and dataprivacy.Fulfiller records that request once:
// one audit entry naming it, with this table's count among the per-section
// counts in its metadata, and one dataprivacy.EventErasureFulfilled. An entry
// and an event here as well would be the request's fan-out across stores
// written down as a separate fact per store, none of which says which erasure
// it belonged to.
//
// A consumer that calls Store.DeleteForUser outside an erasure has a deletion
// nothing else recorded, and embeds this type to override the method.
func (h *RecordingHooks) AfterDeleteForUser(context.Context, database.Tx, tenancy.Scope, string, int64) error {
	return nil
}

// recordToken writes the entry and the event for a write to one token. The
// entry's SubjectID is the token's principal, so a Recorder filing by subject
// can; the principal is not copied into the metadata, for the reason
// recording.Entry.SubjectID gives.
func (h *RecordingHooks) recordToken(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	token *Token,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeToken,
		ResourceID:   token.ID,
		SubjectID:    token.UserID,
		EventType:    auditEventType,
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: token.ID,
		Payload: &TokenEvent{
			TokenID:    token.ID,
			UserID:     token.UserID,
			IssuedAt:   token.CreatedAt,
			ExpiresAt:  token.ExpiresAt,
			RedeemedAt: token.RedeemedAt,
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}
