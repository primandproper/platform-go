package identity

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The resource types this package's audit entries name. They are platform's
// vocabulary for platform's own tables, prefixed so a consumer's "account" is
// never mistaken for one of these.
const (
	// ResourceTypeUser is what an audit entry about a User names.
	ResourceTypeUser = "identity.user"
	// ResourceTypeAccount is what an audit entry about an Account names.
	ResourceTypeAccount = "identity.account"
	// ResourceTypeMembership is what an audit entry about a Membership names.
	ResourceTypeMembership = "identity.membership"
	// ResourceTypeInvitation is what an audit entry about an Invitation names.
	ResourceTypeInvitation = "identity.invitation"
)

// The events this package's operations emit, one per operation. They are
// platform's names for platform's own writes, which is what makes them
// constants here rather than strings a consumer mints.
//
// An operation that writes several rows emits one of these, not one per row: a
// registration is a user, an account and a membership, and a subscriber told
// three times that somebody arrived would act three times. The audit log holds
// the row per resource; the event is the one thing that happened.
//
// A subscriber may receive one only if the dispatcher's catalog knows it and
// does not mark it Internal. [EventCatalog] is the fragment to merge into that
// catalog, and it lists every one of them. The credential events — a password
// changed or its change forced, a second-factor secret issued or proven, a
// verification link minted — are marked Internal: published to the outbox for
// a deployment's own consumers, and offered to no webhook subscriber, because a
// feed of an account's authentication is the one an attacker who has taken it
// over most wants copied somewhere its owner cannot see. signin's credential
// events are marked the same way.
const (
	// EventUserRegistered says somebody registered, with an account of their own
	// or into the one an invitation named.
	EventUserRegistered webhooks.EventType = "identity.user.registered"
	// EventUserArchived says a user was archived, and names the memberships the
	// archival ended.
	EventUserArchived webhooks.EventType = "identity.user.archived"
	// EventUserAccountStatusUpdated says an operator moved a user's standing.
	EventUserAccountStatusUpdated webhooks.EventType = "identity.user.account_status_updated"
	// EventUserServiceRolesUpdated says a user's operator roles were rewritten.
	EventUserServiceRolesUpdated webhooks.EventType = "identity.user.service_roles_updated"
	// EventUserProfileUpdated says a user's profile was saved; the payload names
	// the fields that moved, and never their values.
	EventUserProfileUpdated webhooks.EventType = "identity.user.profile_updated"
	// EventUserAgreementsRecorded says a user accepted one or more documents.
	EventUserAgreementsRecorded webhooks.EventType = "identity.user.agreements_recorded"

	// EventUserPasswordChanged says a user's password was replaced.
	EventUserPasswordChanged webhooks.EventType = "identity.user.password_changed"
	// EventUserPasswordChangeRequirementSet says an operator forced or released
	// a password change; the payload says which.
	EventUserPasswordChangeRequirementSet webhooks.EventType = "identity.user.password_change_requirement_set"
	// EventUserTwoFactorSecretIssued says a user was issued a new, unproven
	// second-factor secret.
	EventUserTwoFactorSecretIssued webhooks.EventType = "identity.user.two_factor_secret_issued"
	// EventUserTwoFactorSecretVerified says a user proved the secret they hold.
	EventUserTwoFactorSecretVerified webhooks.EventType = "identity.user.two_factor_secret_verified"
	// EventUserEmailAddressVerificationIssued says a verification link was
	// minted for a user's address. The secret is not on it: the caller that
	// minted the link holds it.
	EventUserEmailAddressVerificationIssued webhooks.EventType = "identity.user.email_address_verification_issued"
	// EventUserEmailAddressVerified says a user's address is now proven.
	EventUserEmailAddressVerified webhooks.EventType = "identity.user.email_address_verified"
	// EventUserEmailAddressUnverified says a user's address stopped being proven.
	EventUserEmailAddressUnverified webhooks.EventType = "identity.user.email_address_unverified"

	// EventAccountCreated says a user who was already here opened another
	// account.
	EventAccountCreated webhooks.EventType = "identity.account.created"
	// EventAccountUpdated says an account's details were rewritten; the payload
	// names which fields.
	EventAccountUpdated webhooks.EventType = "identity.account.updated"
	// EventAccountOwnershipTransferred says an account changed owner.
	EventAccountOwnershipTransferred webhooks.EventType = "identity.account.ownership_transferred"
	// EventAccountArchived says an account was archived, and names the
	// memberships the archival ended.
	EventAccountArchived webhooks.EventType = "identity.account.archived"

	// EventMembershipDefaultSet says a user's default account moved.
	EventMembershipDefaultSet webhooks.EventType = "identity.membership.default_set"
	// EventMembershipRolesUpdated says a member's roles in an account were
	// rewritten.
	EventMembershipRolesUpdated webhooks.EventType = "identity.membership.roles_updated"
	// EventMembershipRemoved says somebody left or was removed from an account.
	EventMembershipRemoved webhooks.EventType = "identity.membership.removed"

	// EventInvitationCreated says an invitation was issued.
	EventInvitationCreated webhooks.EventType = "identity.invitation.created"
	// EventInvitationAccepted says an existing user accepted an invitation.
	EventInvitationAccepted webhooks.EventType = "identity.invitation.accepted"
	// EventInvitationRejected says the recipient declined an invitation.
	EventInvitationRejected webhooks.EventType = "identity.invitation.rejected"
	// EventInvitationCancelled says the sender withdrew an invitation.
	EventInvitationCancelled webhooks.EventType = "identity.invitation.cancelled"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with, with the credential
// events marked Internal as the constants' block says:
//
//	catalog, err := webhooks.Merge(
//	    webhooks.Catalog{OrderCreated: {Description: "..."}},
//	    identity.EventCatalog(),
//	)
//
// A deployment that does want a credential event delivered clears Internal on
// its own merged copy rather than defining the event again beside this
// fragment, which Merge refuses.
//
// None of them carries a bearer secret, and the two that might have are
// [EventUserRegistered] and [EventInvitationCreated]. A registration's
// verification link promotes the registrant and an invitation's joins an
// account, and an event goes wherever a deployment's catalog sends it — so a
// link on one is a link in every subscriber's logs, delivered by a merge that
// never mentioned it. The links reach the mailbox they were minted for through
// a mailer instead: [InvitationMailer] for an invitation's, and signin's
// VerificationMailer for a registration's. The hook arguments still carry both
// tokens, and a deployment that queues its mail on the operation's transaction
// rather than through a mailer writes that row from a hook of its own, onto a
// topic no subscriber reads.
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventUserRegistered:                     {Description: "Somebody registered."},
		EventUserArchived:                       {Description: "A user was archived."},
		EventUserAccountStatusUpdated:           {Description: "A user's account status changed."},
		EventUserServiceRolesUpdated:            {Description: "A user's service roles changed."},
		EventUserProfileUpdated:                 {Description: "A user's profile changed."},
		EventUserAgreementsRecorded:             {Description: "A user accepted the terms of service or the privacy policy."},
		EventUserPasswordChanged:                {Description: "A user's password changed.", Internal: true},
		EventUserPasswordChangeRequirementSet:   {Description: "A password change was forced on a user, or released.", Internal: true},
		EventUserTwoFactorSecretIssued:          {Description: "A user was issued a new two-factor secret.", Internal: true},
		EventUserTwoFactorSecretVerified:        {Description: "A user verified their two-factor secret.", Internal: true},
		EventUserEmailAddressVerificationIssued: {Description: "A verification link was issued for a user's email address.", Internal: true},
		EventUserEmailAddressVerified:           {Description: "A user's email address was verified."},
		EventUserEmailAddressUnverified:         {Description: "A user's email address verification was withdrawn."},
		EventAccountCreated:                     {Description: "An existing user opened another account."},
		EventAccountUpdated:                     {Description: "An account's details changed."},
		EventAccountOwnershipTransferred:        {Description: "An account changed owner."},
		EventAccountArchived:                    {Description: "An account was archived."},
		EventMembershipDefaultSet:               {Description: "A user's default account changed."},
		EventMembershipRolesUpdated:             {Description: "A member's roles in an account changed."},
		EventMembershipRemoved:                  {Description: "Somebody left or was removed from an account."},
		EventInvitationCreated:                  {Description: "An invitation to join an account was issued."},
		EventInvitationAccepted:                 {Description: "An invitation was accepted."},
		EventInvitationRejected:                 {Description: "An invitation was declined."},
		EventInvitationCancelled:                {Description: "An invitation was withdrawn by its sender."},
	}
}

// UserEvent is the payload of every user event.
//
// It names the user by ID and carries what the operation changed, never the
// row: a subscriber told a user changed reads the user for what they are now,
// and a payload that carried the row would be a copy of a person's details in
// every subscriber's logs. Each field beyond UserID is set by the events it
// documents and empty on the rest.
type UserEvent struct {
	_ struct{} `json:"-"`

	// RequiresPasswordChange is what an operator set, on
	// EventUserPasswordChangeRequirementSet.
	RequiresPasswordChange *bool `json:"requiresPasswordChange,omitempty"`

	// UserID is the user the event is about.
	UserID string `json:"userID"`

	// AccountID and MembershipID name where a registration landed: the
	// registrant's own account, or the one their invitation joined.
	AccountID    string `json:"accountID,omitempty"`
	MembershipID string `json:"membershipID,omitempty"`

	// InvitationID is the invitation a registration answered.
	InvitationID string `json:"invitationID,omitempty"`

	// AccountStatus and PreviousAccountStatus are where the user stands after
	// and before EventUserAccountStatusUpdated.
	AccountStatus         AccountStatus `json:"accountStatus,omitempty"`
	PreviousAccountStatus AccountStatus `json:"previousAccountStatus,omitempty"`

	// Roles and PreviousRoles are the service roles after and before
	// EventUserServiceRolesUpdated, both, because holding a role is not the
	// same fact as gaining it.
	Roles         []string `json:"roles,omitempty"`
	PreviousRoles []string `json:"previousRoles,omitempty"`

	// Changed names the fields an update moved, sorted, and never their values.
	Changed []string `json:"changed,omitempty"`

	// Agreements are the documents accepted, on EventUserAgreementsRecorded.
	Agreements []Agreement `json:"agreements,omitempty"`

	// EndedMemberships are the memberships an archival ended. A subscriber
	// striking the user from its own rosters needs them, and after the commit
	// no read here returns them.
	EndedMemberships []*MembershipEvent `json:"endedMemberships,omitempty"`

	// SatisfiedRequiredChange says a password change completed one an operator
	// had forced, on EventUserPasswordChanged.
	SatisfiedRequiredChange bool `json:"satisfiedRequiredChange,omitempty"`

	// ReplacedVerifiedSecret says a new second-factor secret replaced one the
	// user had proven, on EventUserTwoFactorSecretIssued — which is the case
	// worth alerting on, where an unproven one being replaced is an enrollment
	// restarted.
	ReplacedVerifiedSecret bool `json:"replacedVerifiedSecret,omitempty"`
}

// AccountEvent is the payload of every account event.
//
// It names the account and its owner by ID, and not by name: an account is
// often an organization's, a team's or a person's own, and its name is theirs.
type AccountEvent struct {
	_ struct{} `json:"-"`

	// AccountID is the account the event is about.
	AccountID string `json:"accountID"`
	// OwnerUserID is who owns it after the write.
	OwnerUserID string `json:"ownerUserID"`
	// PreviousOwnerUserID is who owned it before, on
	// EventAccountOwnershipTransferred.
	PreviousOwnerUserID string `json:"previousOwnerUserID,omitempty"`
	// MembershipID is the owner membership that came with a created account.
	MembershipID string `json:"membershipID,omitempty"`

	// Changed names the fields an update moved, sorted. The timestamp every
	// save stamps is left off: a subscriber asking what was edited is told
	// nothing by it.
	Changed []string `json:"changed,omitempty"`

	// EndedMemberships are the memberships an archival ended, for the reason
	// UserEvent.EndedMemberships gives. It is the whole roster: see
	// Hooks.AfterArchiveAccount on what that costs for a large account.
	EndedMemberships []*MembershipEvent `json:"endedMemberships,omitempty"`
}

// MembershipEvent is the payload of every membership event, and how the
// archival events name the memberships they ended.
type MembershipEvent struct {
	_ struct{} `json:"-"`

	// MembershipID is the membership the event is about.
	MembershipID string `json:"membershipID"`
	// UserID is the member.
	UserID string `json:"userID"`
	// AccountID is the account they are a member of.
	AccountID string `json:"accountID"`

	// PreviousAccountID is the default account before EventMembershipDefaultSet,
	// empty when the user had none.
	PreviousAccountID string `json:"previousAccountID,omitempty"`
	// NewDefaultAccountID is where EventMembershipRemoved moved the user's
	// default, empty when it moved nothing.
	NewDefaultAccountID string `json:"newDefaultAccountID,omitempty"`

	// Roles and PreviousRoles are the roles after and before
	// EventMembershipRolesUpdated.
	Roles         []string `json:"roles,omitempty"`
	PreviousRoles []string `json:"previousRoles,omitempty"`
}

// InvitationEvent is the payload of every invitation event.
//
// It names the invitation, the account and the people by ID, and not the
// recipient's address: a subscriber that writes to them reads the invitation.
type InvitationEvent struct {
	_ struct{} `json:"-"`

	// ToUser is who answered it, once somebody has.
	ToUser *string `json:"toUser,omitempty"`

	// InvitationID is the invitation the event is about.
	InvitationID string `json:"invitationID"`
	// AccountID is the account being joined.
	AccountID string `json:"accountID"`
	// FromUser is who sent it.
	FromUser string `json:"fromUser"`
	// Status is where it stands after the write.
	Status InvitationStatus `json:"status"`
	// MembershipID is what an acceptance minted.
	MembershipID string `json:"membershipID,omitempty"`
}

// The metadata keys an audit entry here carries, read back by whoever reads the
// log. Each is a value the column no longer holds, or a relation the entry's
// resource does not name on its own.
const (
	metadataAccountID               = "accountID"
	metadataStatus                  = "status"
	metadataChanged                 = "changed"
	metadataAgreements              = "agreements"
	metadataPreviousOwnerUserID     = "previousOwnerUserID"
	metadataPreviousAccountID       = "previousAccountID"
	metadataNewDefaultAccountID     = "newDefaultAccountID"
	metadataPreviousRoles           = "previousRoles"
	metadataNewRoles                = "newRoles"
	metadataSatisfiedRequiredChange = "satisfiedRequiredChange"
	metadataRequiresPasswordChange  = "requiresPasswordChange"
	metadataReplacedVerifiedSecret  = "replacedVerifiedSecret"
)

// lastUpdatedAtField is the json name of the timestamp every update stamps,
// which a diff therefore always names and a changed-fields list should not.
const lastUpdatedAtField = "lastUpdatedAt"

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. Every operation records an audit
// entry per row it wrote and emits one event, both on the operation's
// transaction, through the recording.Recorder it is built with.
//
// It implements Hooks outright rather than embedding NoopHooks, so an
// operation added to Hooks later fails to compile here until somebody decides
// what it records. That makes it the type a consumer embeds in turn: one that
// wants a single operation recorded differently overrides that method and
// inherits the rest.
//
// The decisions it makes, once, so that no consumer remakes them:
//
//   - An operation that writes several rows records an entry per row and emits
//     one event. An archival records entries for every membership it ended.
//   - A change to who belongs to an account — a membership created, its roles
//     set, removed, or ended by an archival — records two entries, one naming
//     the member as its subject and one naming the account, so a Recorder
//     filing by subject puts it on both chains: the member's, and the one an
//     account's administrator reads to see who joined and who left. A default
//     account set is the member's alone, being nobody else's business.
//   - No secret travels on an entry or an event. The verification token a
//     registration's hook is handed and the token an issued invitation's may be
//     are dropped here, for the reason [EventCatalog] gives; the link goes to
//     the mailbox through a mailer. A consumer that queues its mail from the
//     hook instead embeds this type and writes that row from an override.
//   - A value the column no longer holds goes in the entry's metadata: the
//     previous owner, the previous default, both role sets, and the flags the
//     credential writes cleared on their way past.
//   - A profile save records the names of the fields that moved and no values,
//     as Hooks.AfterUpdateProfile is handed them. An account save and a status
//     change record the diff of the two rows.
//   - The credential operations record that the credential moved and nothing
//     else. The rows are redacted already, and no entry carries a diff of them.
//   - A registration is the registrant's when its request carries nobody, which
//     it ordinarily does not, since the registrant is what it produces. One that
//     carries a principal — an operator registering somebody — is theirs.
//
// What it does not decide is where an entry is filed or, past a registration,
// who made it; both are the Recorder's, through its ScopeResolver and its
// principal extractor. Every entry about a person sets Entry.SubjectID — the
// user, or the member — and every entry about an account names the account, so
// a resolver filing by subject puts an account's own history, its roster's
// comings and goings included, on the account's chain, which is the one a
// member asking about their account reads.
//
// Nor does it revoke anything. Hooks.AfterUpdateUserAccountStatus names the two
// calls that end a suspended user's sessions and refresh-token families, and
// says the choice is the consumer's; a logging type that made it would be making
// a safety decision on their behalf. A consumer that wants the revocation embeds
// this type and overrides that method, calling the embedded one beside it.
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

// AfterRegister records the user, the account and the membership a
// registration wrote — the membership on the member's chain and the account's
// both — and emits one EventUserRegistered. The verification token
// the registration carries is on neither.
func (h *RecordingHooks) AfterRegister(ctx context.Context, tx database.Tx, scope tenancy.Scope, registration *Registration) error {
	if registration == nil || registration.User == nil {
		return ErrNilUser
	}

	if registration.Account == nil {
		return ErrNilAccount
	}

	if registration.Membership == nil {
		return ErrNilMembership
	}

	user, account, membership := registration.User, registration.Account, registration.Membership

	entries := append([]*recording.Entry{
		userEntry(user, audit.EventCreated, nil, nil),
		accountEntry(account, audit.EventCreated, nil, nil),
	}, membershipEntries(membership, audit.EventCreated, nil)...)

	return h.recordRegistration(ctx, tx, scope, user, &UserEvent{
		UserID:       user.ID,
		AccountID:    account.ID,
		MembershipID: membership.ID,
	}, entries...)
}

// recordRegistration emits one EventUserRegistered and writes every entry as
// the registrant, unless the request carries a principal.
//
// A registration is the write that mints the principal, so its request
// ordinarily carries nobody and Record would file all of it as unattributed.
// An operator registering somebody on their behalf sends a request that does
// carry one, and then the entries name the operator rather than the registrant.
func (h *RecordingHooks) recordRegistration(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
	payload *UserEvent,
	entries ...*recording.Entry,
) error {
	event := &webhooks.Event{
		EventType:   EventUserRegistered,
		OrderingKey: user.ID,
		Payload:     payload,
	}

	return h.recorder.RecordOrAs(ctx, tx, scope, audit.Actor{ID: user.ID, Type: audit.ActorUser}, event, entries...)
}

// AfterRegisterWithInvitation records the user the registration wrote, the
// invitation as accepted rather than created, and the membership the answer
// filed, and emits one EventUserRegistered naming the invitation.
func (h *RecordingHooks) AfterRegisterWithInvitation(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	registration *InvitedRegistration,
) error {
	if registration == nil || registration.User == nil {
		return ErrNilUser
	}

	if registration.Invitation == nil {
		return ErrNilInvitation
	}

	if registration.Membership == nil {
		return ErrNilMembership
	}

	user, invitation, membership := registration.User, registration.Invitation, registration.Membership

	entries := append([]*recording.Entry{
		userEntry(user, audit.EventCreated, nil, nil),
		invitationEntry(invitation, audit.EventUpdated),
	}, membershipEntries(membership, audit.EventCreated, nil)...)

	return h.recordRegistration(ctx, tx, scope, user, &UserEvent{
		UserID:       user.ID,
		AccountID:    membership.BelongsToAccount,
		MembershipID: membership.ID,
		InvitationID: invitation.ID,
	}, entries...)
}

// AfterInvite records the invitation issued, and emits EventInvitationCreated.
// Both are built from the invitation's identifiers, so neither sees the token
// the Service hands this hook when it was built without an InvitationMailer.
func (h *RecordingHooks) AfterInvite(ctx context.Context, tx database.Tx, scope tenancy.Scope, invitation *Invitation) error {
	if invitation == nil {
		return ErrNilInvitation
	}

	return h.record(ctx, tx, scope, EventInvitationCreated, invitation.ID, invitationEvent(invitation),
		invitationEntry(invitation, audit.EventCreated),
	)
}

// AfterAcceptInvitation records the invitation answered and the membership the
// answer minted, on the member's chain and the account's.
func (h *RecordingHooks) AfterAcceptInvitation(ctx context.Context, tx database.Tx, scope tenancy.Scope, acceptance *Acceptance) error {
	if acceptance == nil || acceptance.Invitation == nil {
		return ErrNilInvitation
	}

	if acceptance.Membership == nil {
		return ErrNilMembership
	}

	payload := invitationEvent(acceptance.Invitation)
	payload.MembershipID = acceptance.Membership.ID

	entries := append([]*recording.Entry{
		invitationEntry(acceptance.Invitation, audit.EventUpdated),
	}, membershipEntries(acceptance.Membership, audit.EventCreated, nil)...)

	return h.record(ctx, tx, scope, EventInvitationAccepted, acceptance.Invitation.ID, payload, entries...)
}

// AfterRejectInvitation records the recipient declining.
func (h *RecordingHooks) AfterRejectInvitation(ctx context.Context, tx database.Tx, scope tenancy.Scope, invitation *Invitation) error {
	return h.recordInvitationAnswer(ctx, tx, scope, invitation, EventInvitationRejected)
}

// AfterCancelInvitation records the sender withdrawing.
func (h *RecordingHooks) AfterCancelInvitation(ctx context.Context, tx database.Tx, scope tenancy.Scope, invitation *Invitation) error {
	return h.recordInvitationAnswer(ctx, tx, scope, invitation, EventInvitationCancelled)
}

// AfterCreateAccount records the account and its owner membership, the
// membership on the owner's chain and the account's.
func (h *RecordingHooks) AfterCreateAccount(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	account *Account,
	membership *Membership,
) error {
	if account == nil {
		return ErrNilAccount
	}

	if membership == nil {
		return ErrNilMembership
	}

	entries := append([]*recording.Entry{
		accountEntry(account, audit.EventCreated, nil, nil),
	}, membershipEntries(membership, audit.EventCreated, nil)...)

	return h.record(ctx, tx, scope, EventAccountCreated, account.ID, &AccountEvent{
		AccountID:    account.ID,
		OwnerUserID:  account.OwnerUserID,
		MembershipID: membership.ID,
	}, entries...)
}

// AfterTransferAccountOwnership records the account under its new owner, with
// the previous one in the metadata, since the column holds the new one now. A
// transfer is about the account and about the two people it moved between, so
// it records the account's entry and one more naming each of them as its
// subject: a Recorder filing by subject puts the change on all three chains,
// and one filing by write puts all three on the write's.
func (h *RecordingHooks) AfterTransferAccountOwnership(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	account *Account,
	previousOwnerUserID string,
) error {
	if account == nil {
		return ErrNilAccount
	}

	metadata := map[string]string{metadataPreviousOwnerUserID: previousOwnerUserID}
	entries := []*recording.Entry{accountEntry(account, audit.EventUpdated, nil, metadata)}

	for _, owner := range []string{account.OwnerUserID, previousOwnerUserID} {
		if owner == "" {
			continue
		}

		entry := accountEntry(account, audit.EventUpdated, nil, maps.Clone(metadata))
		entry.SubjectID = owner
		entries = append(entries, entry)
	}

	return h.record(ctx, tx, scope, EventAccountOwnershipTransferred, account.ID, &AccountEvent{
		AccountID:           account.ID,
		OwnerUserID:         account.OwnerUserID,
		PreviousOwnerUserID: previousOwnerUserID,
	}, entries...)
}

// AfterSetDefaultAccount records the membership that is now the default, with
// the account that was in the metadata when there was one.
func (h *RecordingHooks) AfterSetDefaultAccount(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	membership *Membership,
	previousAccountID string,
) error {
	if membership == nil {
		return ErrNilMembership
	}

	payload := membershipEvent(membership)
	payload.PreviousAccountID = previousAccountID

	return h.record(ctx, tx, scope, EventMembershipDefaultSet, membership.BelongsToUser, payload,
		membershipEntry(membership, audit.EventUpdated, nonEmpty(metadataPreviousAccountID, previousAccountID)),
	)
}

// AfterArchiveUser records the user archived and each membership the archival
// ended, on the user's chain and its account's, so every account the user was
// in sees them leave, and emits one EventUserArchived
// naming them.
func (h *RecordingHooks) AfterArchiveUser(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
	endedMemberships []*Membership,
) error {
	if user == nil {
		return ErrNilUser
	}

	ended, entries, err := endMemberships(endedMemberships)
	if err != nil {
		return err
	}

	entries = slices.Insert(entries, 0, userEntry(user, audit.EventArchived, nil, nil))

	return h.record(ctx, tx, scope, EventUserArchived, user.ID, &UserEvent{
		UserID:           user.ID,
		EndedMemberships: ended,
	}, entries...)
}

// AfterArchiveAccount records the account archived and each membership the
// archival ended, on its member's chain and the account's, so a
// Recorder filing by subject puts the account's end on the chain of everybody
// it ended for, and each ending on the account's own. It emits one
// EventAccountArchived naming them.
func (h *RecordingHooks) AfterArchiveAccount(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	account *Account,
	endedMemberships []*Membership,
) error {
	if account == nil {
		return ErrNilAccount
	}

	ended, entries, err := endMemberships(endedMemberships)
	if err != nil {
		return err
	}

	entries = slices.Insert(entries, 0, accountEntry(account, audit.EventArchived, nil, nil))

	return h.record(ctx, tx, scope, EventAccountArchived, account.ID, &AccountEvent{
		AccountID:        account.ID,
		OwnerUserID:      account.OwnerUserID,
		EndedMemberships: ended,
	}, entries...)
}

// AfterUpdateUserAccountStatus records the diff of the two redacted rows, so a
// reinstatement can say what the suspension was for.
//
// It revokes nothing; see the type's documentation for why, and for how a
// consumer that wants the revocation adds it.
func (h *RecordingHooks) AfterUpdateUserAccountStatus(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	before, after *User,
) error {
	if before == nil || after == nil {
		return ErrNilUser
	}

	changes, err := audit.Diff(before.Redacted(), after.Redacted())
	if err != nil {
		return platformerrors.Wrap(err, "diffing the user's account status")
	}

	return h.record(ctx, tx, scope, EventUserAccountStatusUpdated, after.ID, &UserEvent{
		UserID:                after.ID,
		AccountStatus:         after.AccountStatus,
		PreviousAccountStatus: before.AccountStatus,
		Changed:               changedFields(changes),
	},
		userEntry(after, audit.EventUpdated, changes, nil),
	)
}

// AfterSetUserServiceRoles records both role sets, because holding a role is
// not gaining it.
func (h *RecordingHooks) AfterSetUserServiceRoles(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
	previousRoles []string,
) error {
	if user == nil {
		return ErrNilUser
	}

	return h.record(ctx, tx, scope, EventUserServiceRolesUpdated, user.ID, &UserEvent{
		UserID:        user.ID,
		Roles:         slices.Clone(user.ServiceRoles),
		PreviousRoles: slices.Clone(previousRoles),
	},
		userEntry(user, audit.EventUpdated, nil, roleMetadata(previousRoles, user.ServiceRoles)),
	)
}

// AfterUpdateProfile records the names of the fields that moved, and no values:
// the hook is handed none, and Hooks.AfterUpdateProfile says why that is the
// decision rather than a gap.
func (h *RecordingHooks) AfterUpdateProfile(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
	changed []string,
) error {
	if user == nil {
		return ErrNilUser
	}

	names := slices.Sorted(slices.Values(changed))

	return h.record(ctx, tx, scope, EventUserProfileUpdated, user.ID, &UserEvent{
		UserID:  user.ID,
		Changed: names,
	},
		userEntry(user, audit.EventUpdated, nil, map[string]string{metadataChanged: strings.Join(names, ",")}),
	)
}

// AfterUpdateAccount records the diff of the two rows. The event names the
// fields that moved and leaves off the timestamp every save stamps.
func (h *RecordingHooks) AfterUpdateAccount(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	before, after *Account,
) error {
	if before == nil || after == nil {
		return ErrNilAccount
	}

	changes, err := audit.Diff(before, after)
	if err != nil {
		return platformerrors.Wrap(err, "diffing the updated account")
	}

	return h.record(ctx, tx, scope, EventAccountUpdated, after.ID, &AccountEvent{
		AccountID:   after.ID,
		OwnerUserID: after.OwnerUserID,
		Changed:     changedFields(changes),
	},
		accountEntry(after, audit.EventUpdated, changes, nil),
	)
}

// AfterRecordAgreement records which documents the user accepted. When is the
// row's, stamped by the write.
func (h *RecordingHooks) AfterRecordAgreement(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
	agreements []Agreement,
) error {
	if user == nil {
		return ErrNilUser
	}

	accepted := slices.Sorted(slices.Values(agreements))

	names := make([]string, 0, len(accepted))
	for _, agreement := range accepted {
		names = append(names, string(agreement))
	}

	return h.record(ctx, tx, scope, EventUserAgreementsRecorded, user.ID, &UserEvent{
		UserID:     user.ID,
		Agreements: accepted,
	},
		userEntry(user, audit.EventUpdated, nil, map[string]string{metadataAgreements: strings.Join(names, ",")}),
	)
}

// AfterSetMembershipRoles records both role sets, for the reason
// AfterSetUserServiceRoles does, on the member's chain and the account's.
func (h *RecordingHooks) AfterSetMembershipRoles(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	membership *Membership,
	previousRoles []string,
) error {
	if membership == nil {
		return ErrNilMembership
	}

	payload := membershipEvent(membership)
	payload.Roles = slices.Clone(membership.Roles)
	payload.PreviousRoles = slices.Clone(previousRoles)

	return h.record(ctx, tx, scope, EventMembershipRolesUpdated, membership.BelongsToUser, payload,
		membershipEntries(membership, audit.EventUpdated, roleMetadata(previousRoles, membership.Roles))...,
	)
}

// AfterRemoveMembership records the membership ended, on the member's chain and
// the account's, with the account the user's default moved to in the metadata
// when the removal moved one.
func (h *RecordingHooks) AfterRemoveMembership(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	membership *Membership,
	newDefaultAccountID string,
) error {
	if membership == nil {
		return ErrNilMembership
	}

	payload := membershipEvent(membership)
	payload.NewDefaultAccountID = newDefaultAccountID

	return h.record(ctx, tx, scope, EventMembershipRemoved, membership.BelongsToUser, payload,
		membershipEntries(membership, audit.EventArchived, nonEmpty(metadataNewDefaultAccountID, newDefaultAccountID))...,
	)
}

// AfterUpdateUserPassword records the rotation and whether it satisfied a
// change an operator had forced.
func (h *RecordingHooks) AfterUpdateUserPassword(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
	previouslyRequiredChange bool,
) error {
	if user == nil {
		return ErrNilUser
	}

	return h.record(ctx, tx, scope, EventUserPasswordChanged, user.ID, &UserEvent{
		UserID:                  user.ID,
		SatisfiedRequiredChange: previouslyRequiredChange,
	},
		userEntry(user, audit.EventUpdated, nil, map[string]string{
			metadataSatisfiedRequiredChange: strconv.FormatBool(previouslyRequiredChange),
		}),
	)
}

// AfterSetUserRequiresPasswordChange records whether an operator forced the
// change or released it.
func (h *RecordingHooks) AfterSetUserRequiresPasswordChange(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *User) error {
	if user == nil {
		return ErrNilUser
	}

	required := user.RequiresPasswordChange

	return h.record(ctx, tx, scope, EventUserPasswordChangeRequirementSet, user.ID, &UserEvent{
		UserID:                 user.ID,
		RequiresPasswordChange: &required,
	},
		userEntry(user, audit.EventUpdated, nil, map[string]string{
			metadataRequiresPasswordChange: strconv.FormatBool(required),
		}),
	)
}

// AfterUpdateUserTwoFactorSecret records the issue and whether it replaced a
// secret the user had proven.
func (h *RecordingHooks) AfterUpdateUserTwoFactorSecret(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
	previousSecretVerifiedAt *time.Time,
) error {
	if user == nil {
		return ErrNilUser
	}

	replaced := previousSecretVerifiedAt != nil

	return h.record(ctx, tx, scope, EventUserTwoFactorSecretIssued, user.ID, &UserEvent{
		UserID:                 user.ID,
		ReplacedVerifiedSecret: replaced,
	},
		userEntry(user, audit.EventUpdated, nil, map[string]string{
			metadataReplacedVerifiedSecret: strconv.FormatBool(replaced),
		}),
	)
}

// AfterMarkUserTwoFactorSecretVerified records the proof.
func (h *RecordingHooks) AfterMarkUserTwoFactorSecretVerified(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
) error {
	return h.recordCredential(ctx, tx, scope, user, EventUserTwoFactorSecretVerified)
}

// AfterSetUserEmailAddressVerificationToken records that a link was minted.
// The token is not handed to the hook, so neither the entry nor the event can
// carry it.
func (h *RecordingHooks) AfterSetUserEmailAddressVerificationToken(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
) error {
	return h.recordCredential(ctx, tx, scope, user, EventUserEmailAddressVerificationIssued)
}

// AfterMarkUserEmailAddressVerified records the address being proven.
func (h *RecordingHooks) AfterMarkUserEmailAddressVerified(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
) error {
	return h.recordCredential(ctx, tx, scope, user, EventUserEmailAddressVerified)
}

// AfterMarkUserEmailAddressUnverified records the proof being withdrawn.
func (h *RecordingHooks) AfterMarkUserEmailAddressUnverified(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
) error {
	return h.recordCredential(ctx, tx, scope, user, EventUserEmailAddressUnverified)
}

// record emits one event keyed on orderingKey and writes every entry, through
// the Recorder.
func (h *RecordingHooks) record(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	eventType webhooks.EventType,
	orderingKey string,
	payload any,
	entries ...*recording.Entry,
) error {
	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: orderingKey,
		Payload:     payload,
	}

	return h.recorder.Record(ctx, tx, scope, event, entries...)
}

// recordInvitationAnswer records a status write on an invitation that minted
// nothing.
func (h *RecordingHooks) recordInvitationAnswer(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	invitation *Invitation,
	eventType webhooks.EventType,
) error {
	if invitation == nil {
		return ErrNilInvitation
	}

	return h.record(ctx, tx, scope, eventType, invitation.ID, invitationEvent(invitation),
		invitationEntry(invitation, audit.EventUpdated),
	)
}

// recordCredential records a credential write that says nothing beyond that
// it happened: the entry names the user and carries no metadata, and the row is
// redacted already.
func (h *RecordingHooks) recordCredential(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *User,
	eventType webhooks.EventType,
) error {
	if user == nil {
		return ErrNilUser
	}

	return h.record(ctx, tx, scope, eventType, user.ID, &UserEvent{UserID: user.ID},
		userEntry(user, audit.EventUpdated, nil, nil),
	)
}

// userEntry is an entry about a user, naming them as its subject. It reads the
// user's ID and nothing else off the row, so a Registration's User, which still
// carries whatever credentials the caller assembled it with, hands the entry
// none of them.
func userEntry(user *User, eventType audit.EventType, changes map[string]audit.Change, metadata map[string]string) *recording.Entry {
	return &recording.Entry{
		ResourceType: ResourceTypeUser,
		ResourceID:   user.ID,
		SubjectID:    user.ID,
		EventType:    eventType,
		Changes:      changes,
		Metadata:     metadata,
	}
}

// accountEntry is an entry about an account, naming the account as its
// subject. An account is not a person, but it is what the entry concerns and
// it owns a chain of its own: filed by subject, its creation, its edits and its
// archival land where its members read its history, rather than on whichever
// chain the directory's writes run in. The people on it are named by their
// memberships' entries, so the account's chain is never one a member's
// erasure resolves to.
func accountEntry(account *Account, eventType audit.EventType, changes map[string]audit.Change, metadata map[string]string) *recording.Entry {
	return &recording.Entry{
		ResourceType: ResourceTypeAccount,
		ResourceID:   account.ID,
		SubjectID:    account.ID,
		EventType:    eventType,
		Changes:      changes,
		Metadata:     metadata,
	}
}

// membershipEntries are the two entries a change to who belongs to an account
// owes: membershipEntry's, on the member's chain, and the same entry naming the
// account as its subject, on the account's. Somebody joining or leaving is a
// fact about both — "which accounts was I in" is the member's question, and
// "who joined us, and who left" is the first one an account's administrator
// asks of its log — so it is filed on both, as a transfer is filed on the
// account's chain and both owners'. Filed by write, the two are the same
// write's, on its chain.
//
// The account's copy names the member by the membership it is about and by
// nothing else, for the reason recording.Entry.SubjectID gives for keeping a
// person's ID out of metadata: a departed member's erasure leaves the copy on
// a surviving account's chain, and an ID it could not count would outlive them
// unreported.
func membershipEntries(membership *Membership, eventType audit.EventType, extra map[string]string) []*recording.Entry {
	member := membershipEntry(membership, eventType, extra)

	account := membershipEntry(membership, eventType, extra)
	account.SubjectID = membership.BelongsToAccount

	return []*recording.Entry{member, account}
}

// membershipEntry is an entry about a membership, naming the member as its
// subject and the account in its metadata, beside whatever extra says. On its
// own it is a change only the member's chain needs — which account is their
// default — and membershipEntries is the pair for everything else.
func membershipEntry(membership *Membership, eventType audit.EventType, extra map[string]string) *recording.Entry {
	metadata := map[string]string{metadataAccountID: membership.BelongsToAccount}
	maps.Copy(metadata, extra)

	return &recording.Entry{
		ResourceType: ResourceTypeMembership,
		ResourceID:   membership.ID,
		SubjectID:    membership.BelongsToUser,
		EventType:    eventType,
		Metadata:     metadata,
	}
}

// invitationEntry is an entry about an invitation, carrying the account it
// joins and the status the write left it in. It names the recipient as its
// subject once there is one. Before an answer the recipient is an address, and
// no entry here carries an address, so the invitation is the account's until it
// is somebody's: its issue, and a cancellation or refusal that leaves it
// unanswered, are filed by subject on the account's chain, beside the account's
// own entries, where whoever administers the account reads who was invited and
// when. Nor does it read the token, which is the point of building it field by
// field.
func invitationEntry(invitation *Invitation, eventType audit.EventType) *recording.Entry {
	subject := invitation.BelongsToAccount
	if invitation.ToUser != nil {
		subject = *invitation.ToUser
	}

	return &recording.Entry{
		ResourceType: ResourceTypeInvitation,
		ResourceID:   invitation.ID,
		SubjectID:    subject,
		EventType:    eventType,
		Metadata: map[string]string{
			metadataAccountID: invitation.BelongsToAccount,
			metadataStatus:    invitation.Status.String(),
		},
	}
}

// invitationEvent is the payload naming an invitation, without its token.
func invitationEvent(invitation *Invitation) *InvitationEvent {
	var toUser *string
	if invitation.ToUser != nil {
		answered := *invitation.ToUser
		toUser = &answered
	}

	return &InvitationEvent{
		InvitationID: invitation.ID,
		AccountID:    invitation.BelongsToAccount,
		FromUser:     invitation.FromUser,
		ToUser:       toUser,
		Status:       invitation.Status,
	}
}

// membershipEvent is the payload naming a membership.
func membershipEvent(membership *Membership) *MembershipEvent {
	return &MembershipEvent{
		MembershipID: membership.ID,
		UserID:       membership.BelongsToUser,
		AccountID:    membership.BelongsToAccount,
	}
}

// endMemberships is the payload and the archived entries for each membership
// an archival ended.
func endMemberships(memberships []*Membership) ([]*MembershipEvent, []*recording.Entry, error) {
	events := make([]*MembershipEvent, 0, len(memberships))
	entries := make([]*recording.Entry, 0, 2*len(memberships)+1)

	for _, membership := range memberships {
		if membership == nil {
			return nil, nil, ErrNilMembership
		}

		events = append(events, membershipEvent(membership))
		entries = append(entries, membershipEntries(membership, audit.EventArchived, nil)...)
	}

	return events, entries, nil
}

// roleMetadata records both role sets, sorted and comma-joined.
func roleMetadata(previous, current []string) map[string]string {
	return map[string]string{
		metadataPreviousRoles: strings.Join(slices.Sorted(slices.Values(previous)), ","),
		metadataNewRoles:      strings.Join(slices.Sorted(slices.Values(current)), ","),
	}
}

// nonEmpty is a one-key metadata map, or nil when value is empty.
func nonEmpty(key, value string) map[string]string {
	if value == "" {
		return nil
	}

	return map[string]string{key: value}
}

// changedFields names the fields a diff says moved, sorted, without the
// timestamp every save stamps. It is nil for a nil diff.
func changedFields(changes map[string]audit.Change) []string {
	if len(changes) == 0 {
		return nil
	}

	fields := slices.Sorted(maps.Keys(changes))

	return slices.DeleteFunc(fields, func(field string) bool { return field == lastUpdatedAtField })
}
