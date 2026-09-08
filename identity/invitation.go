package identity

import (
	"context"
	"slices"
	"time"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// InvitationStatus is where an invitation stands.
type InvitationStatus string

const (
	// InvitationPending is an invitation that has been sent and not answered.
	InvitationPending InvitationStatus = "pending"

	// InvitationAccepted is one the recipient took up. The membership it
	// produced is written in the same transaction, so an accepted invitation
	// without a membership is not a state this package can leave behind.
	InvitationAccepted InvitationStatus = "accepted"

	// InvitationRejected is one the recipient declined.
	InvitationRejected InvitationStatus = "rejected"

	// InvitationCancelled is one the sender withdrew. It is distinct from
	// rejected because the two are answered by different people, and "did they
	// say no or did we change our mind" is the question the sender is asking
	// when they look.
	InvitationCancelled InvitationStatus = "cancelled"
)

// Valid reports whether s is one of the four statuses.
func (s InvitationStatus) Valid() bool {
	switch s {
	case InvitationPending, InvitationAccepted, InvitationRejected, InvitationCancelled:
		return true
	default:
		return false
	}
}

// String renders the status as it is stored.
func (s InvitationStatus) String() string { return string(s) }

// Pending reports whether the invitation is still waiting on an answer. Every
// other status is one it can no longer move out of.
func (s InvitationStatus) Pending() bool { return s == InvitationPending }

// Invitation is an offer of membership in an account, addressed to an email
// address rather than to a user.
//
// Addressed to an address, deliberately: the common case is inviting somebody
// who has not registered yet, and an invitation that could only name an
// existing user would make "invite a colleague" a two-step flow with a
// registration in the middle. ToUser is filled in when it is accepted, which is
// the first moment there is a user to name.
type Invitation struct {
	_ struct{} `json:"-"`

	// CreatedAt is when the invitation was sent, stamped by the Store.
	CreatedAt time.Time `json:"createdAt"`

	// LastUpdatedAt is when it was answered.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when it was retired.
	ArchivedAt *time.Time `json:"archivedAt"`

	// ExpiresAt is when the invitation stops being acceptable. It is required:
	// an invitation link is a bearer credential for joining somebody else's
	// account, and one that never expires is one that is still valid in a
	// mailbox somebody lost control of two years ago.
	ExpiresAt time.Time `json:"expiresAt"`

	// ToUser is the user who accepted, nil until somebody does.
	ToUser *string `json:"toUser"`

	// Scope is whose directory this invitation is in.
	Scope tenancy.Scope `json:"scope"`

	// ID identifies the invitation. It travels in the link beside the token,
	// because looking an invitation up by token alone makes the token an index
	// key and a timing oracle; naming the row and then comparing the token means
	// the comparison happens on one row.
	ID string `json:"id"`

	// BelongsToAccount is the account being joined.
	BelongsToAccount string `json:"belongsToAccount"`

	// FromUser is who sent it.
	FromUser string `json:"fromUser"`

	// ToEmail is the address it was sent to.
	ToEmail string `json:"toEmail"`

	// ToName is what to call the recipient in the email. Optional.
	ToName string `json:"toName"`

	// Token is the secret half of the link. It is excluded from this type's JSON
	// rendering and, like the credential fields on User, that tag binds
	// encoding/json alone — see Redacted.
	Token string `json:"-"`

	// Status is where the invitation stands.
	Status InvitationStatus `json:"status"`

	// Note is what the sender wrote when they sent the invitation — the message
	// rendered into the invite email and shown beside the invitation on a
	// roster. It is written once, at creation, and no answer touches it.
	Note string `json:"note"`

	// StatusNote is why the invitation was answered the way it was, written by
	// whoever answered it. It is empty until somebody does, and it is a second
	// column rather than the first one reused: an answer that overwrote Note
	// would destroy the sender's message at the moment it became the thing a
	// roster wants to show beside the answer.
	//
	// Only the two status writes assign it — AcceptInvitation and
	// SetInvitationStatus — which is why an invitation carrying one at creation
	// is refused: there is no flow that could have answered it.
	StatusNote string `json:"statusNote"`

	// Roles are the roles the resulting membership will carry. They are fixed at
	// invitation time rather than at acceptance so that what somebody is being
	// invited to is what they get, and so the email can say so.
	Roles []string `json:"roles"`
}

// Expired reports whether the invitation's window has closed as of now.
func (i *Invitation) Expired(now time.Time) bool {
	return i != nil && !now.Before(i.ExpiresAt)
}

// Redacted returns a copy of the invitation with the token cleared, for the
// same reason User.Redacted exists: the struct tag binds one codec, and an
// invitation is a value applications routinely render and cache.
func (i *Invitation) Redacted() *Invitation {
	if i == nil {
		return nil
	}

	clone := *i
	clone.Token = ""

	return &clone
}

// EnsureDefaults fills an Invitation's optional fields.
func (i *Invitation) EnsureDefaults() {
	if i == nil {
		return
	}

	if i.Status == "" {
		i.Status = InvitationPending
	}
}

var _ validation.ValidatableWithContext = (*Invitation)(nil)

// ValidateWithContext checks an Invitation's invariants before it is written.
func (i *Invitation) ValidateWithContext(ctx context.Context) error {
	if i == nil {
		return ErrNilInvitation
	}

	if err := i.Scope.Validate(); err != nil {
		return err
	}

	if !i.Status.Valid() {
		return platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "invitation status %q", i.Status)
	}

	if i.ExpiresAt.IsZero() {
		return platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "invitation has no expiry")
	}

	if slices.Contains(i.Roles, "") {
		return platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "invitation carries an empty role name")
	}

	return validation.ValidateStructWithContext(ctx, i,
		validation.Field(&i.BelongsToAccount, validation.Required),
		validation.Field(&i.FromUser, validation.Required),
		validation.Field(&i.ToEmail, validation.Required, emailAddressRule),
		validation.Field(&i.Token, validation.Required),
		validation.Field(&i.Roles, validation.Required),
	)
}
