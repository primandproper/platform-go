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
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type left
// out of it is still published to the outbox and dispatched to nobody.
const (
	// EventUserRegistered says somebody registered, with an account of their own
	// or into the one an invitation named. The payload carries the verification
	// link's secret when the caller minted one.
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

	// EventInvitationCreated says an invitation was issued. The payload carries
	// the link's secret when the Service hands it to the hook, which is when it
	// was built without an InvitationMailer.
	EventInvitationCreated webhooks.EventType = "identity.invitation.created"
	// EventInvitationAccepted says an existing user accepted an invitation.
	EventInvitationAccepted webhooks.EventType = "identity.invitation.accepted"
	// EventInvitationRejected says the recipient declined an invitation.
	EventInvitationRejected webhooks.EventType = "identity.invitation.rejected"
	// EventInvitationCancelled says the sender withdrew an invitation.
	EventInvitationCancelled webhooks.EventType = "identity.invitation.cancelled"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with:
//
//	catalog := webhooks.Catalog{OrderCreated: {Description: "..."}}
//	maps.Copy(catalog, identity.EventCatalog())
//
// Two of them carry a bearer secret — [EventUserRegistered] the verification
// link's and [EventInvitationCreated] the invitation's — because the outbox
// consumer that mails the link has nowhere else to take it from. A deployment
// that subscribes an endpoint to either is handing that endpoint the link; one
// that does not want that leaves the two out of the catalog it merges, and the
// outbox still carries them.
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
		EventUserPasswordChanged:                {Description: "A user's password changed."},
		EventUserPasswordChangeRequirementSet:   {Description: "A password change was forced on a user, or released."},
		EventUserTwoFactorSecretIssued:          {Description: "A user was issued a new two-factor secret."},
		EventUserTwoFactorSecretVerified:        {Description: "A user verified their two-factor secret."},
		EventUserEmailAddressVerificationIssued: {Description: "A verification link was issued for a user's email address."},
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

	// EmailAddressVerificationToken is the secret a registrant's verification
	// link carries, on EventUserRegistered when the caller minted one. It is
	// here and on no audit entry: the column holds a digest, so this is the one
	// place the outbox consumer mailing the link can read it from.
	EmailAddressVerificationToken string `json:"emailAddressVerificationToken,omitempty"`

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
// often a household or a person's own, and its name is theirs.
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

	// Token is the secret half of the link, on EventInvitationCreated when the
	// Service handed it to the hook. It is here and on no audit entry, for the
	// reason UserEvent.EmailAddressVerificationToken is.
	Token string `json:"token,omitempty"`
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
//     one event. An archival records an entry per membership it ended, each
//     naming that member as its subject, so a Recorder filing by subject puts it
//     on their chain.
//   - Secrets travel on the event and never on an entry. The verification token
//     a registration carries and the token an issued invitation carries are
//     what the outbox consumer builds the link from; see [EventCatalog] for what
//     that means for a webhook subscriber. No entry is ever handed one.
//   - A value the column no longer holds goes in the entry's metadata: the
//     previous owner, the previous default, both role sets, and the flags the
//     credential writes cleared on their way past.
//   - A profile save records the names of the fields that moved and no values,
//     as Hooks.AfterUpdateProfile is handed them. An account save and a status
//     change record the diff of the two rows.
//   - The credential operations record that the credential moved and nothing
//     else. The rows are redacted already, and no entry carries a diff of them.
//
// What it does not decide is where an entry is filed or who made it; both are
// the Recorder's, through its ScopeResolver and its principal extractor. Every
// entry about a person sets Entry.SubjectID — the user, or the member — so a
// resolver filing by subject can; an account's entries name none.
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
// registration wrote, and emits one EventUserRegistered carrying the
// verification token, which no entry sees.
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

	return h.record(ctx, tx, scope, EventUserRegistered, user.ID, &UserEvent{
		UserID:                        user.ID,
		AccountID:                     account.ID,
		MembershipID:                  membership.ID,
		EmailAddressVerificationToken: registration.EmailAddressVerificationToken,
	},
		userEntry(user, audit.EventCreated, nil, nil),
		accountEntry(account, audit.EventCreated, nil, nil),
		membershipEntry(membership, audit.EventCreated, nil),
	)
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

	return h.record(ctx, tx, scope, EventUserRegistered, user.ID, &UserEvent{
		UserID:                        user.ID,
		AccountID:                     membership.BelongsToAccount,
		MembershipID:                  membership.ID,
		InvitationID:                  invitation.ID,
		EmailAddressVerificationToken: registration.EmailAddressVerificationToken,
	},
		userEntry(user, audit.EventCreated, nil, nil),
		invitationEntry(invitation, audit.EventUpdated),
		membershipEntry(membership, audit.EventCreated, nil),
	)
}

// AfterInvite records the invitation issued, and emits EventInvitationCreated
// carrying its token when the Service handed it one. The entry is built from
// the invitation's identifiers and never sees the token.
func (h *RecordingHooks) AfterInvite(ctx context.Context, tx database.Tx, scope tenancy.Scope, invitation *Invitation) error {
	if invitation == nil {
		return ErrNilInvitation
	}

	payload := invitationEvent(invitation)
	payload.Token = invitation.Token

	return h.record(ctx, tx, scope, EventInvitationCreated, invitation.ID, payload,
		invitationEntry(invitation, audit.EventCreated),
	)
}

// AfterAcceptInvitation records the invitation answered and the membership the
// answer minted.
func (h *RecordingHooks) AfterAcceptInvitation(ctx context.Context, tx database.Tx, scope tenancy.Scope, acceptance *Acceptance) error {
	if acceptance == nil || acceptance.Invitation == nil {
		return ErrNilInvitation
	}

	if acceptance.Membership == nil {
		return ErrNilMembership
	}

	payload := invitationEvent(acceptance.Invitation)
	payload.MembershipID = acceptance.Membership.ID

	return h.record(ctx, tx, scope, EventInvitationAccepted, acceptance.Invitation.ID, payload,
		invitationEntry(acceptance.Invitation, audit.EventUpdated),
		membershipEntry(acceptance.Membership, audit.EventCreated, nil),
	)
}

// AfterRejectInvitation records the recipient declining.
func (h *RecordingHooks) AfterRejectInvitation(ctx context.Context, tx database.Tx, scope tenancy.Scope, invitation *Invitation) error {
	return h.recordInvitationAnswer(ctx, tx, scope, invitation, EventInvitationRejected)
}

// AfterCancelInvitation records the sender withdrawing.
func (h *RecordingHooks) AfterCancelInvitation(ctx context.Context, tx database.Tx, scope tenancy.Scope, invitation *Invitation) error {
	return h.recordInvitationAnswer(ctx, tx, scope, invitation, EventInvitationCancelled)
}

// AfterCreateAccount records the account and its owner membership.
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

	return h.record(ctx, tx, scope, EventAccountCreated, account.ID, &AccountEvent{
		AccountID:    account.ID,
		OwnerUserID:  account.OwnerUserID,
		MembershipID: membership.ID,
	},
		accountEntry(account, audit.EventCreated, nil, nil),
		membershipEntry(membership, audit.EventCreated, nil),
	)
}

// AfterTransferAccountOwnership records the account under its new owner, with
// the previous one in the metadata, since the column holds the new one now.
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

	return h.record(ctx, tx, scope, EventAccountOwnershipTransferred, account.ID, &AccountEvent{
		AccountID:           account.ID,
		OwnerUserID:         account.OwnerUserID,
		PreviousOwnerUserID: previousOwnerUserID,
	},
		accountEntry(account, audit.EventUpdated, nil, map[string]string{
			metadataPreviousOwnerUserID: previousOwnerUserID,
		}),
	)
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

// AfterArchiveUser records the user archived and one entry per membership the
// archival ended, and emits one EventUserArchived naming them.
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

// AfterArchiveAccount records the account archived and one entry per
// membership the archival ended, each naming its member as the subject, so a
// Recorder filing by subject puts the account's end on the chain of everybody
// it ended for. It emits one EventAccountArchived naming them.
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
// AfterSetUserServiceRoles does.
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
		membershipEntry(membership, audit.EventUpdated, roleMetadata(previousRoles, membership.Roles)),
	)
}

// AfterRemoveMembership records the membership ended, with the account the
// user's default moved to in the metadata when the removal moved one.
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
		membershipEntry(membership, audit.EventArchived, nonEmpty(metadataNewDefaultAccountID, newDefaultAccountID)),
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

// accountEntry is an entry about an account. It names no subject: an account
// is not a person, and the people on it are named by their memberships'
// entries.
func accountEntry(account *Account, eventType audit.EventType, changes map[string]audit.Change, metadata map[string]string) *recording.Entry {
	return &recording.Entry{
		ResourceType: ResourceTypeAccount,
		ResourceID:   account.ID,
		EventType:    eventType,
		Changes:      changes,
		Metadata:     metadata,
	}
}

// membershipEntry is an entry about a membership, naming the member as its
// subject and the account in its metadata, beside whatever extra says.
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
// subject once there is one; before an answer, the recipient is an address,
// and no entry here carries an address. Nor does it read the token, which is
// the point of building it field by field.
func invitationEntry(invitation *Invitation, eventType audit.EventType) *recording.Entry {
	entry := &recording.Entry{
		ResourceType: ResourceTypeInvitation,
		ResourceID:   invitation.ID,
		EventType:    eventType,
		Metadata: map[string]string{
			metadataAccountID: invitation.BelongsToAccount,
			metadataStatus:    invitation.Status.String(),
		},
	}

	if invitation.ToUser != nil {
		entry.SubjectID = *invitation.ToUser
	}

	return entry
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

// endMemberships is the payload and the archived entry for each membership an
// archival ended.
func endMemberships(memberships []*Membership) ([]*MembershipEvent, []*recording.Entry, error) {
	events := make([]*MembershipEvent, 0, len(memberships))
	entries := make([]*recording.Entry, 0, len(memberships)+1)

	for _, membership := range memberships {
		if membership == nil {
			return nil, nil, ErrNilMembership
		}

		events = append(events, membershipEvent(membership))
		entries = append(entries, membershipEntry(membership, audit.EventArchived, nil))
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
