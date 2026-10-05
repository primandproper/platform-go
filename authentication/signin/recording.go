package signin

import (
	"context"
	"strconv"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/identity"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ResourceTypeUser is what every audit entry this package's hooks record names.
// Each one is about a user the directory owns, whichever credential of theirs
// moved, so the resource is the user and the entry's ResourceID is their ID.
const ResourceTypeUser = "identity.user"

// The events this package's hooks emit, one per hook that publishes. They are
// platform's names for platform's own writes, as waitlists' are.
//
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type left
// out of it is still published to the outbox and dispatched to nobody, which is
// how a deployment keeps a credential event internal.
//
// The password, second-factor and recovery-code events are credential events,
// and EventCatalog leaves them out: they are recorded and published to the
// outbox, where a deployment's own consumers may read them, and offered to no
// webhook subscriber. passkeys' credential events are gated the same way, for
// the reason its EventCatalog gives. A deployment that does want a third party
// told when somebody's sign-in methods change adds them to its catalog by name.
const (
	// EventUserAuthenticated says somebody proved a credential, through any
	// door. The payload says which kind, whether the door was administrative,
	// and on an impersonation which operator.
	EventUserAuthenticated webhooks.EventType = "signin.user.authenticated"
	// EventPasswordUpdated says a user changed their password.
	EventPasswordUpdated webhooks.EventType = "signin.password.updated"
	// EventPasswordAttached says a user who held no password was given one.
	EventPasswordAttached webhooks.EventType = "signin.password.attached"
	// EventTOTPSecretRefreshed says a user was issued a new second-factor
	// secret, and holds no proven second factor until they verify it.
	//nolint:gosec // G101: an event name; the secret is never on the event.
	EventTOTPSecretRefreshed webhooks.EventType = "signin.totp_secret.refreshed"
	// EventTOTPSecretVerified says a user proved the second-factor secret they
	// hold.
	//nolint:gosec // G101: an event name; the secret is never on the event.
	EventTOTPSecretVerified webhooks.EventType = "signin.totp_secret.verified"
	// EventEmailAddressVerified says a user answered the verification link
	// mailed to their address.
	EventEmailAddressVerified webhooks.EventType = "signin.email_address.verified"
	// EventVerificationEmailRequested says a user was minted a fresh
	// verification link.
	EventVerificationEmailRequested webhooks.EventType = "signin.verification_email.requested"
	// EventMagicLinkRequested says a user was minted a sign-in link.
	EventMagicLinkRequested webhooks.EventType = "signin.magic_link.requested"
	// EventRecoveryCodeUsed says a user spent one of their recovery codes. The
	// payload says how many are left.
	EventRecoveryCodeUsed webhooks.EventType = "signin.recovery_code.used"
	// EventRecoveryCodesReplaced says a user was issued a fresh set of recovery
	// codes, withdrawing the set before it.
	EventRecoveryCodesReplaced webhooks.EventType = "signin.recovery_codes.replaced"
	// EventSignInsRevoked says one or more of a user's logins were ended. The
	// payload names each and the door that ended them.
	EventSignInsRevoked webhooks.EventType = "signin.sign_ins.revoked"
	// EventSignInAccountSwitched says a login moved to another of its user's
	// accounts.
	EventSignInAccountSwitched webhooks.EventType = "signin.sign_in.account_switched"
)

// EventCatalog is every event this package emits that a webhook subscriber may
// receive, described, for a consumer to merge into the catalog its dispatcher is
// built with. It leaves out the credential events, as the constants' block says:
//
//	catalog := webhooks.Catalog{OrderCreated: {Description: "..."}}
//	maps.Copy(catalog, signin.EventCatalog())
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventUserAuthenticated:          {Description: "Somebody proved a credential and signed in, or an operator was issued a token to act as them."},
		EventEmailAddressVerified:       {Description: "A user answered the verification link mailed to their address."},
		EventVerificationEmailRequested: {Description: "A user was minted a fresh email verification link."},
		EventMagicLinkRequested:         {Description: "A user was minted a sign-in link."},
		EventSignInsRevoked:             {Description: "One or more of a user's logins were ended."},
		EventSignInAccountSwitched:      {Description: "A login moved to another of its user's accounts."},
	}
}

// UserEvent is the payload of every event that says only that something
// happened to one user's credentials.
//
// It names the user and nothing else. No event here carries a secret, a code,
// a link or its digest: the hooks are never handed one, and an event is copied
// into every subscriber's logs. A mailer that sends the link listens on the
// mail the service sends after the commit, not here.
type UserEvent struct {
	_ struct{} `json:"-"`

	// UserID is whose credential moved.
	UserID string `json:"userID"`
}

// AuthenticationEvent is the payload of [EventUserAuthenticated].
type AuthenticationEvent struct {
	_ struct{} `json:"-"`

	// UserID is who authenticated, or on an impersonation who is being acted
	// as.
	UserID string `json:"userID"`
	// AccountID is the account the principal was resolved against.
	AccountID string `json:"accountID,omitempty"`
	// CredentialKind is the door: what proved it.
	CredentialKind CredentialKind `json:"credentialKind"`
	// ActorID is the operator on an impersonation, and empty otherwise.
	ActorID string `json:"actorID,omitempty"`
	// ActorScope is the scope ActorID is in, and the zero Scope whenever
	// ActorID is empty.
	ActorScope tenancy.Scope `json:"actorScope,omitzero"`
	// Administrative reports whether the door was the administrative one.
	Administrative bool `json:"administrative"`
}

// VerificationEvent is the payload of [EventEmailAddressVerified].
type VerificationEvent struct {
	_ struct{} `json:"-"`

	// UserID is whose address was proven.
	UserID string `json:"userID"`
	// Promoted reports whether proving it moved the user's status.
	Promoted bool `json:"promoted"`
}

// RecoveryCodeEvent is the payload of [EventRecoveryCodeUsed].
type RecoveryCodeEvent struct {
	_ struct{} `json:"-"`

	// UserID is who spent the code.
	UserID string `json:"userID"`
	// Remaining is how many unspent codes they hold now.
	Remaining int `json:"remaining"`
}

// RevocationEvent is the payload of [EventSignInsRevoked].
type RevocationEvent struct {
	_ struct{} `json:"-"`

	// UserID is whose logins they were.
	UserID string `json:"userID"`
	// ActorID is who asked, as [Revocation.ActorID] says.
	ActorID string `json:"actorID,omitempty"`
	// Reason is the door that ended them.
	Reason RevocationReason `json:"reason"`
	// FamilyIDs names each login ended.
	FamilyIDs []string `json:"familyIDs"`
}

// AccountSwitchEvent is the payload of [EventSignInAccountSwitched].
type AccountSwitchEvent struct {
	_ struct{} `json:"-"`

	// UserID is whose login moved.
	UserID string `json:"userID"`
	// FamilyID is the login that moved.
	FamilyID string `json:"familyID"`
	// AccountID is the account it is for now.
	AccountID string `json:"accountID"`
	// PreviousAccountID is the account it was for until now, and empty for a
	// login that began against no account.
	PreviousAccountID string `json:"previousAccountID,omitempty"`
	// Administrative reports whether the login came through the
	// administrative door.
	Administrative bool `json:"administrative"`
}

// The metadata keys an audit entry here carries. Every entry carries
// metadataEvent, because most of them are audit.EventUpdated on the same
// resource type and the event name is what tells a password change from a
// second factor moving.
const (
	metadataEvent = "event"
	//nolint:gosec // G101: a metadata key naming the kind of credential, never one.
	metadataCredentialKind    = "credentialKind"
	metadataAdministrative    = "administrative"
	metadataAccountID         = "accountID"
	metadataPreviousAccountID = "previousAccountID"
	metadataImpersonatorScope = "impersonatorScope"
	metadataPromoted          = "promoted"
	metadataRemaining         = "remaining"
	metadataReason            = "reason"
	metadataRevoked           = "revoked"
)

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. Each hook that records writes one
// audit entry about the user, with ResourceType [ResourceTypeUser] and the
// user's ID as both ResourceID and Entry.SubjectID, and emits the event above
// for it, both on the operation's transaction, through the recording.Recorder
// it is built with.
//
// It implements Hooks outright rather than embedding NoopHooks, so a hook added
// to the interface later fails to compile here until somebody decides what it
// records. The two that record nothing, AfterIssueToken and AfterFailedSignIn,
// are methods that say so and why, rather than an embedded no-op that says
// nothing. That makes it the type a consumer embeds in turn: one that wants a
// single entry shaped differently overrides that method and inherits the rest.
//
// # Who the actor is
//
// Every hook but AfterAuthenticate takes the actor off the context, through the
// Recorder's principal extractor, as every store's RecordingHooks does. A door
// a person reaches signed in — a password change, a second factor — records
// them; a door reached by holding a mailed link, or in the middle of a sign-in,
// records audit.ActorUnattributed, because nobody was signed in, and the entry's
// resource still names the user.
//
// AfterAuthenticate is the exception, through recording.Recorder.RecordAs. The
// request that proves a credential carries no principal, because the principal
// is what proving it produces, so the context would file every login as
// unattributed. The service has just proven who it is and hands the hook the
// principal, and the entry names them. An impersonation is recorded as the
// operator's act: the entry's actor is the subject with the operator in the
// audit.Actor.Impersonator slot, which is how audit.PrincipalActor spells a
// delegated principal, so the entry is filed under the user and
// audit.Query.ImpersonatorID finds it among what the operator did.
//
// # Where an entry is filed
//
// The write's scope, unless the Recorder's ScopeResolver says otherwise. Every
// entry sets SubjectID to the user, so a resolver that files by subject can.
// No entry names the user in its metadata, for the reason waitlists'
// recordSignup gives: an identifier there outlives audit.Erasure uncounted.
type RecordingHooks struct {
	recorder *recording.Recorder
}

var _ Hooks = (*RecordingHooks)(nil)

// NewRecordingHooks builds the Hooks that record every sign-in write through
// recorder.
func NewRecordingHooks(recorder *recording.Recorder) (*RecordingHooks, error) {
	if recorder == nil {
		return nil, ErrNilRecorder
	}

	return &RecordingHooks{recorder: recorder}, nil
}

// AfterAuthenticate records a login, whatever door it came through. It is the
// one place "logged in" is recorded: every door runs it, the token-less ones
// included, which is why AfterIssueToken records nothing beside it.
//
// The entry is audit.EventOther, because a login changes nothing about the user
// and audit.EventAccessed is the read-auditing vocabulary a deployment may route
// to a table of its own; the metadata's event name says what it was. Its actor is the principal the
// service proved, and on an impersonation the subject with the operator as
// impersonator; see the type's documentation. The operator's scope goes in the
// metadata, because the impersonator slot holds an ID and an operator in
// another tenant is otherwise ambiguous.
func (h *RecordingHooks) AfterAuthenticate(ctx context.Context, tx database.Tx, scope tenancy.Scope, auth *Authentication) error {
	if auth == nil || auth.Principal == nil || auth.Principal.User == nil {
		return ErrNilHookArgument
	}

	userID := auth.Principal.User.ID

	metadata := map[string]string{
		metadataEvent:          EventUserAuthenticated.String(),
		metadataCredentialKind: string(auth.CredentialKind),
		metadataAdministrative: strconv.FormatBool(auth.Administrative),
	}

	if auth.Principal.ActiveAccountID != "" {
		metadata[metadataAccountID] = auth.Principal.ActiveAccountID
	}

	actor := audit.Actor{ID: userID, Type: audit.ActorUser, Impersonator: auth.ActorID}

	if auth.ActorID != "" {
		metadata[metadataImpersonatorScope] = auth.ActorScope.String()
	}

	entry := userEntry(userID, audit.EventOther, metadata)

	event := &webhooks.Event{
		EventType:   EventUserAuthenticated,
		OrderingKey: userID,
		Payload: &AuthenticationEvent{
			UserID:         userID,
			AccountID:      auth.Principal.ActiveAccountID,
			CredentialKind: auth.CredentialKind,
			ActorID:        auth.ActorID,
			ActorScope:     auth.ActorScope,
			Administrative: auth.Administrative,
		},
	}

	return h.recorder.RecordAs(ctx, tx, scope, actor, event, entry)
}

// AfterIssueToken records nothing. A token is minted per sign-in and per
// refresh, so an entry for each would bury the log under traffic, and
// AfterAuthenticate has already recorded the login the token belongs to. The
// token's ID is a revocation list's concern, not the audit log's.
func (*RecordingHooks) AfterIssueToken(context.Context, database.Tx, tenancy.Scope, *SignIn) error {
	return nil
}

// AfterFailedSignIn records nothing. A failed login is not yet something
// platform keeps per user, and a lockout that counted them would belong on both
// doors at once — this one and passkeys' — rather than in one hook's audit
// entries. It is the same decision, in the same words, as passkeys'
// RecordingHooks makes for AfterFailedPasskeyLogin.
func (*RecordingHooks) AfterFailedSignIn(context.Context, database.Tx, tenancy.Scope, *FailedSignIn) error {
	return nil
}

// AfterUpdatePassword records a password change. Nothing about the password is
// on the entry or the event; the user handed to the hook is redacted and does
// not carry the hash.
func (h *RecordingHooks) AfterUpdatePassword(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *identity.User) error {
	return h.recordUser(ctx, tx, scope, user, EventPasswordUpdated)
}

// AfterRefreshTOTPSecret records a new second-factor secret being issued. The
// secret is never on the entry or the event; the hook is not handed it.
func (h *RecordingHooks) AfterRefreshTOTPSecret(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *identity.User) error {
	return h.recordUser(ctx, tx, scope, user, EventTOTPSecretRefreshed)
}

// AfterAttachPassword records a first password being given to a user who held
// none. The link that authorized it is never on the entry or the event.
func (h *RecordingHooks) AfterAttachPassword(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *identity.User) error {
	return h.recordUser(ctx, tx, scope, user, EventPasswordAttached)
}

// AfterVerify records an address proven through the link door, and nothing for
// [Service.CompleteVerification]. That door promotes somebody on a proof the
// consumer asked for and says nothing about the address, so recording it as a
// verified address would be a claim it does not make; the consumer whose proof
// it was records it.
//
// A second click on one link runs the hook too, with Promoted false, and is
// recorded: somebody answered the link again, and the metadata says nothing
// moved.
func (h *RecordingHooks) AfterVerify(ctx context.Context, tx database.Tx, scope tenancy.Scope, verification *Verification) error {
	if verification == nil {
		return ErrNilHookArgument
	}

	if !verification.EmailAddressProven {
		return nil
	}

	if verification.User == nil {
		return identity.ErrNilUser
	}

	userID := verification.User.ID

	entry := userEntry(userID, audit.EventUpdated, map[string]string{
		metadataEvent:    EventEmailAddressVerified.String(),
		metadataPromoted: strconv.FormatBool(verification.Promoted),
	})

	event := &webhooks.Event{
		EventType:   EventEmailAddressVerified,
		OrderingKey: userID,
		Payload:     &VerificationEvent{UserID: userID, Promoted: verification.Promoted},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// AfterRequestVerificationEmail records a verification link being minted. The
// token is never on the entry or the event: the hook is not handed it, and the
// mail carrying it is sent by the service after this transaction commits.
func (h *RecordingHooks) AfterRequestVerificationEmail(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *identity.User) error {
	return h.recordUser(ctx, tx, scope, user, EventVerificationEmailRequested)
}

// AfterRequestMagicLink records a sign-in link being minted. The link is never
// on the entry or the event, for AfterRequestVerificationEmail's reason. It runs
// only for an address somebody holds, so neither half is an oracle: both are the
// deployment's own records, and the caller is answered the same either way.
func (h *RecordingHooks) AfterRequestMagicLink(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *identity.User) error {
	return h.recordUser(ctx, tx, scope, user, EventMagicLinkRequested)
}

// AfterVerifyTOTPSecret records a user proving the second-factor secret they
// hold.
func (h *RecordingHooks) AfterVerifyTOTPSecret(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *identity.User) error {
	return h.recordUser(ctx, tx, scope, user, EventTOTPSecretVerified)
}

// AfterRecoveryCodeUsed records a recovery code being spent, and how many the
// user has left. The code is never on the entry or the event.
//
// This records; it does not tell the person. The mail Hooks.AfterRecoveryCodeUsed
// asks for is a subscriber to [EventRecoveryCodeUsed], which is the outbox row
// that documentation means.
func (h *RecordingHooks) AfterRecoveryCodeUsed(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *identity.User,
	remaining int,
) error {
	if user == nil {
		return identity.ErrNilUser
	}

	entry := userEntry(user.ID, audit.EventUpdated, map[string]string{
		metadataEvent:     EventRecoveryCodeUsed.String(),
		metadataRemaining: strconv.Itoa(remaining),
	})

	event := &webhooks.Event{
		EventType:   EventRecoveryCodeUsed,
		OrderingKey: user.ID,
		Payload:     &RecoveryCodeEvent{UserID: user.ID, Remaining: remaining},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// AfterReplaceRecoveryCodes records a fresh set of recovery codes being issued.
// The codes are never on the entry or the event.
func (h *RecordingHooks) AfterReplaceRecoveryCodes(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *identity.User) error {
	return h.recordUser(ctx, tx, scope, user, EventRecoveryCodesReplaced)
}

// AfterRevokeSignIns records logins being ended: one entry about the user with
// the door and the count, and an event naming each login.
//
// One entry rather than one per login, because it is one act — a sign-out
// everywhere is one decision however many devices it reaches — and the event
// carries the family IDs for a subscriber that wants each.
func (h *RecordingHooks) AfterRevokeSignIns(ctx context.Context, tx database.Tx, scope tenancy.Scope, revocation *Revocation) error {
	if revocation == nil {
		return ErrNilHookArgument
	}

	entry := userEntry(revocation.SubjectID, audit.EventUpdated, map[string]string{
		metadataEvent:   EventSignInsRevoked.String(),
		metadataReason:  string(revocation.Reason),
		metadataRevoked: strconv.Itoa(len(revocation.FamilyIDs)),
	})

	event := &webhooks.Event{
		EventType:   EventSignInsRevoked,
		OrderingKey: revocation.SubjectID,
		Payload: &RevocationEvent{
			UserID:    revocation.SubjectID,
			ActorID:   revocation.ActorID,
			Reason:    revocation.Reason,
			FamilyIDs: revocation.FamilyIDs,
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// AfterSwitchAccount records a login moving to another of its user's accounts,
// with the account it left as previousAccountID. It is recorded here rather than
// inferred from the token AfterIssueToken is handed next, because nothing on a
// SignIn says which account came before.
func (h *RecordingHooks) AfterSwitchAccount(ctx context.Context, tx database.Tx, scope tenancy.Scope, change *AccountSwitch) error {
	if change == nil {
		return ErrNilHookArgument
	}

	metadata := map[string]string{
		metadataEvent:          EventSignInAccountSwitched.String(),
		metadataAccountID:      change.ToAccountID,
		metadataAdministrative: strconv.FormatBool(change.Administrative),
	}

	if change.FromAccountID != "" {
		metadata[metadataPreviousAccountID] = change.FromAccountID
	}

	entry := userEntry(change.SubjectID, audit.EventUpdated, metadata)

	event := &webhooks.Event{
		EventType:   EventSignInAccountSwitched,
		OrderingKey: change.SubjectID,
		Payload: &AccountSwitchEvent{
			UserID:            change.SubjectID,
			FamilyID:          change.FamilyID,
			AccountID:         change.ToAccountID,
			PreviousAccountID: change.FromAccountID,
			Administrative:    change.Administrative,
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// recordUser writes the entry and the event for a credential write that says
// nothing beyond which user and what moved.
func (h *RecordingHooks) recordUser(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *identity.User,
	eventType webhooks.EventType,
) error {
	if user == nil {
		return identity.ErrNilUser
	}

	entry := userEntry(user.ID, audit.EventUpdated, map[string]string{metadataEvent: eventType.String()})

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: user.ID,
		Payload:     &UserEvent{UserID: user.ID},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// userEntry is the one shape every entry here has: about the user, filed where
// a ScopeResolver reading SubjectID says.
func userEntry(userID string, eventType audit.EventType, metadata map[string]string) *recording.Entry {
	return &recording.Entry{
		ResourceType: ResourceTypeUser,
		ResourceID:   userID,
		SubjectID:    userID,
		EventType:    eventType,
		Metadata:     metadata,
	}
}
