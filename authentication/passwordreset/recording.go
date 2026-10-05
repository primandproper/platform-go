package passwordreset

import (
	"context"
	"strconv"
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
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type
// left out of it is still published to the outbox and dispatched to nobody.
const (
	// EventTokenIssued says a reset was asked for and a link minted for it. The
	// payload names the token and the principal, and never carries the secret.
	EventTokenIssued webhooks.EventType = "passwordreset.token.issued"
	// EventTokenRedeemed says a link was spent.
	EventTokenRedeemed webhooks.EventType = "passwordreset.token.redeemed"
	// EventTokensErased says a principal's tokens were deleted by an erasure.
	// The payload carries the count, and nothing that identifies whose.
	EventTokensErased webhooks.EventType = "passwordreset.tokens.erased"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with:
//
//	catalog := webhooks.Catalog{OrderCreated: {Description: "..."}}
//	maps.Copy(catalog, passwordreset.EventCatalog())
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventTokenIssued:   {Description: "A password reset was asked for, and a link minted for it."},
		EventTokenRedeemed: {Description: "A password reset link was spent."},
		EventTokensErased:  {Description: "A principal's password reset tokens were deleted by an erasure."},
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

// ErasureEvent is the payload of EventTokensErased. It says how many tokens an
// erasure deleted and not whose.
//
// The reason is the one waitlists.ErasureEvent gives: "forget this person" is
// one signal per subject, and a per-store erasure event naming them would be
// that signal repeated once for every store the person touched. This event
// says only that an erasure ran here and how much it found.
type ErasureEvent struct {
	_ struct{} `json:"-"`

	// Deleted is how many tokens the erasure deleted, zero included.
	Deleted int64 `json:"deleted"`
}

// metadataDeleted is the audit metadata key an erasure's entry carries its count
// under.
const metadataDeleted = "deleted"

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. An issuance, a redemption and an
// erasure each record an audit entry and emit the event above for it, both on
// the write's transaction, through the recording.Recorder it is built with. A
// revocation records nothing, for the reason AfterRevokeForUser gives.
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

// AfterDeleteForUser records an erasure: that it ran, and how many tokens it
// deleted. Zero is recorded too, because the erasure ran.
//
// It names no principal, neither as the entry's subject nor in its metadata
// nor on the event. The entry is about the erasure, so it is filed under the
// write's scope and survives audit.Erasure's deletion of the subject's own
// scopes; a principal's identifier on it would be the one reference the erasure
// left behind, uncounted in what the subject is told is retained. The count is
// what a reader of the log can be told.
func (h *RecordingHooks) AfterDeleteForUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, _ string, deleted int64) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeToken,
		EventType:    audit.EventDeleted,
		Metadata:     map[string]string{metadataDeleted: strconv.FormatInt(deleted, 10)},
	}

	event := &webhooks.Event{
		EventType: EventTokensErased,
		Payload:   &ErasureEvent{Deleted: deleted},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
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
