package identity

import (
	"context"
	"slices"
	"time"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Membership is a user's place in an account: that they belong to it, what they
// may do there, and whether it is the one they land in.
//
// It is a row rather than a column on either side because it is a many-to-many
// with facts of its own. Storing it as an array of account IDs on the user
// makes "who is in this account" a scan, and gives the roles nowhere to live.
type Membership struct {
	_ struct{} `json:"-"`

	// CreatedAt is when the user joined, stamped by the Store.
	CreatedAt time.Time `json:"createdAt"`

	// LastUpdatedAt is when the roles or the default flag last changed.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when the user left or was removed. The row is kept rather
	// than deleted: "who had access to this account in March" is asked most
	// often after somebody has gone.
	ArchivedAt *time.Time `json:"archivedAt"`

	// ID identifies the membership.
	ID string `json:"id"`

	// BelongsToUser is the member.
	BelongsToUser string `json:"belongsToUser"`

	// BelongsToAccount is the account they are a member of.
	BelongsToAccount string `json:"belongsToAccount"`

	// Scope is whose directory this membership is in. It matches the user's and
	// the account's — a membership across two directories is not a thing this
	// package can express, which is the point.
	Scope tenancy.Scope `json:"scope"`

	// Roles are the role names this user holds in this account, resolved to
	// permissions by the authorization package's PolicyResolver.
	//
	// They are strings rather than an authorization.PermissionSet because the
	// mapping from role to permission is policy, and policy changes without the
	// membership rows changing. Storing the expanded permissions would freeze
	// every user's access at the moment they were granted a role, and a policy
	// edit would then have to rewrite every row that mentions it.
	Roles []string `json:"roles"`

	// DefaultAccount marks the account this user lands in when they sign in
	// without naming one. Exactly one of a user's live memberships carries it,
	// which the Store enforces on write rather than trusting callers to.
	DefaultAccount bool `json:"defaultAccount"`
}

// Archived reports whether the membership has ended.
func (m *Membership) Archived() bool { return m != nil && m.ArchivedAt != nil }

var _ validation.ValidatableWithContext = (*Membership)(nil)

// ValidateWithContext checks a Membership's invariants before it is written.
//
// Roles are required. A membership with none is a user who belongs to an
// account and may do nothing in it, which is never what the caller meant and
// reads at runtime as an authorization bug rather than as a missing field.
func (m *Membership) ValidateWithContext(ctx context.Context) error {
	if m == nil {
		return ErrNilMembership
	}

	if err := m.Scope.Validate(); err != nil {
		return err
	}

	if slices.Contains(m.Roles, "") {
		return platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "membership carries an empty role name")
	}

	return validation.ValidateStructWithContext(ctx, m,
		validation.Field(&m.BelongsToUser, validation.Required),
		validation.Field(&m.BelongsToAccount, validation.Required),
		validation.Field(&m.Roles, validation.Required),
	)
}

// MembershipWithUser is a membership joined to the member it names.
//
// It is the shape an account's roster is read in, because rendering one means
// showing who the members are rather than which IDs they have — and reading the
// users separately would be one query per row.
//
// The User is redacted by the Store before it is returned: a roster is the read
// most likely to reach a response body, and a page of thirty users is thirty
// chances to leak a password hash.
type MembershipWithUser struct {
	_ struct{} `json:"-"`

	// User is the member.
	User *User `json:"user"`

	Membership
}
