package identity

import (
	"context"
	"slices"
	"time"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// AccountStatus is where a user stands with the operator: whether they may sign
// in at all, and if not, why not.
//
// It is a named string rather than a string so that the four states are a
// closed set a switch can be exhaustive over, and so that a column holding
// "banned " with a trailing space fails Valid rather than quietly meaning
// nothing. It is not a permission — what a user may do once admitted is the
// authorization package's question, resolved from their membership roles.
type AccountStatus string

const (
	// StatusUnverified is a user who has registered and not yet verified what
	// registration required of them. It is the status CreateUser assigns when
	// none is set, because a user who has proven nothing yet is the honest
	// default and admitting them by default is the mistake worth making
	// unspellable.
	StatusUnverified AccountStatus = "unverified"

	// StatusGood is a user in good standing.
	StatusGood AccountStatus = "good"

	// StatusBanned is a user an operator has suspended. It is reversible, and
	// the explanation is meant to be shown to them.
	StatusBanned AccountStatus = "banned"

	// StatusTerminated is a user whose access has ended permanently. It is not
	// erasure — the row remains, because an account with a payment history and
	// an audit trail cannot simply vanish. See Store.EraseUser for the erasure
	// a right-to-be-forgotten request actually performs.
	StatusTerminated AccountStatus = "terminated"
)

// Valid reports whether s is one of the four statuses.
func (s AccountStatus) Valid() bool {
	switch s {
	case StatusUnverified, StatusGood, StatusBanned, StatusTerminated:
		return true
	default:
		return false
	}
}

// String renders the status as it is stored.
func (s AccountStatus) String() string { return string(s) }

// AdmitsSignIn reports whether a user in this status may authenticate.
//
// Only StatusGood does. It is a method rather than a comparison at each call
// site because "which statuses may sign in" is a rule that can be got wrong
// twice, and the second copy is the one that admits a banned user.
func (s AccountStatus) AdmitsSignIn() bool { return s == StatusGood }

// User is somebody who can sign in.
//
// The credential fields are here rather than in a table of their own — see the
// package documentation for why. They are excluded from this type's JSON
// rendering, but a struct tag only binds encoding/json: hand a User to any
// other codec and the hash goes with it. Redacted is what a response body
// should carry.
type User struct {
	_ struct{} `json:"-"`

	// CreatedAt is when the user registered. It is set by the Store on write,
	// from its clock, so every row in the directory is stamped by one source.
	CreatedAt time.Time `json:"createdAt"`

	// LastUpdatedAt is when any field last changed, or nil for a user who has
	// not been edited since registration.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when the user was soft-deleted. A archived user is excluded
	// from every read that does not ask for archived rows, and is still present
	// for the audit trail and for the foreign keys that reference them.
	ArchivedAt *time.Time `json:"archivedAt"`

	// EmailAddressVerifiedAt is when the address below was proven reachable, or
	// nil if it has not been.
	//
	// Like the token it travels with, it is not writable through UpdateUser and
	// the field is ignored there: a proof is something the store records when a
	// link comes back, and a caller able to assign it could verify an address by
	// saving a profile.
	EmailAddressVerifiedAt *time.Time `json:"emailAddressVerifiedAt"`

	// PasswordLastChangedAt is when HashedPassword last changed. It is what a
	// password-age policy reads, and nil for a user still on the hash they
	// registered with.
	PasswordLastChangedAt *time.Time `json:"passwordLastChangedAt"`

	// TwoFactorSecretVerifiedAt is when the user first proved they held
	// TwoFactorSecret. A secret that has been issued and never proven is not a
	// second factor — it is a QR code somebody may have closed — so this being
	// nil is what a sign-in flow checks, not whether the secret is non-empty.
	TwoFactorSecretVerifiedAt *time.Time `json:"twoFactorSecretVerifiedAt"`

	// LastAcceptedTermsOfService and LastAcceptedPrivacyPolicy are when the user
	// last agreed to each document. They are timestamps rather than booleans
	// because the question an operator actually has is "who has not accepted the
	// version published on the 3rd", which a boolean cannot answer.
	LastAcceptedTermsOfService *time.Time `json:"lastAcceptedTermsOfService"`
	LastAcceptedPrivacyPolicy  *time.Time `json:"lastAcceptedPrivacyPolicy"`

	// ID identifies the user.
	ID string `json:"id"`

	// Username is the handle the user signs in with, unique within Scope.
	Username string `json:"username"`

	// EmailAddress is the address the user is reachable at, unique within Scope.
	EmailAddress string `json:"emailAddress"`

	// FirstName and LastName are the user's name as they gave it. Both are
	// optional: plenty of applications never ask, and plenty of people do not
	// have a name that splits in two.
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`

	// AccountStatus is where the user stands with the operator.
	AccountStatus AccountStatus `json:"accountStatus"`

	// AccountStatusExplanation is why, in prose meant for the user.
	AccountStatusExplanation string `json:"accountStatusExplanation"`

	// HashedPassword is what the argon2 package produced. This package never
	// hashes and never compares; it stores the string the engine returned.
	//
	// It is empty for a user who holds no password credential — a passkey-only
	// or federated registration — which is a supported user rather than an
	// unfinished one. Ask HasPassword rather than reading this field, and see
	// the package documentation for what the empty string must never be taken
	// to mean.
	HashedPassword string `json:"-"`

	// TwoFactorSecret is the TOTP shared secret, as the totp package generated
	// it. See TwoFactorSecretVerifiedAt for why holding one is not the same as
	// having a second factor.
	TwoFactorSecret string `json:"-"`

	// EmailAddressVerificationToken is the value a verification link carries. It
	// is cleared when the address is verified, so a link cannot be replayed
	// after it has worked once, and it is cleared again whenever EmailAddress
	// moves: the column records that a link was mailed and not which address it
	// went to, so a token that outlived the address it was minted for would
	// prove the one that replaced it.
	//
	// Writing it through UpdateUser is not possible, and the field is ignored
	// there. Store.SetUserEmailAddressVerificationToken issues one and
	// Store.MarkUserEmailAddressVerified burns it.
	EmailAddressVerificationToken string `json:"-"`

	// Scope is whose directory this user is in. See the package documentation:
	// it is not the account.
	Scope tenancy.Scope `json:"scope"`

	// ServiceRoles are the roles this user holds outside any account —
	// operator, support, service administrator — resolved to permissions by the
	// authorization package's PolicyResolver alongside their membership roles.
	//
	// They are separate from Membership.Roles because the two are granted by
	// different people and answer different questions: a support engineer's role
	// does not make them a member of the account they are looking at, and
	// conflating the two would put them on its roster.
	//
	// Every read that returns a User fills these in. A field that were populated
	// on some reads and not others would be indistinguishable from a user who
	// genuinely holds none, and the reading that fails open is the one where an
	// authorization check finds an empty set.
	ServiceRoles []string `json:"serviceRoles"`

	// RequiresPasswordChange forces a password change at next sign-in. It is set
	// by an operator resetting a compromised credential, and cleared by
	// Store.UpdateUserPassword.
	RequiresPasswordChange bool `json:"requiresPasswordChange"`
}

// EnsureDefaults fills a User's optional fields.
//
// It defaults the status to StatusUnverified rather than StatusGood: a user who
// has proven nothing is the honest starting point, and an application with no
// verification step says StatusGood explicitly.
func (u *User) EnsureDefaults() {
	if u == nil {
		return
	}

	if u.AccountStatus == "" {
		u.AccountStatus = StatusUnverified
	}
}

var _ validation.ValidatableWithContext = (*User)(nil)

// ValidateWithContext checks a User's invariants before it is written.
//
// The scope is checked here, with the rest, rather than being left to the
// Store: a user that says nothing about which directory it belongs to is one an
// application registered by accident. Say tenancy.Global to mean it.
//
// The password hash is neither required nor shape-checked. Not shape-checked
// because that would mean this package knowing which hashing engine produced
// it, which is exactly the coupling the engines were kept free of. Not required
// because a user need not have a password at all — see the package
// documentation on passwordless users. Requiring it was meant to catch the
// caller who forgot to hash, and never could: a plaintext password is a
// non-empty string and passed.
func (u *User) ValidateWithContext(ctx context.Context) error {
	if u == nil {
		return ErrNilUser
	}

	if err := u.Scope.Validate(); err != nil {
		return err
	}

	if !u.AccountStatus.Valid() {
		return platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "account status %q", u.AccountStatus)
	}

	return u.validateProfile(ctx)
}

// validateProfile checks the invariants of the columns a profile write assigns,
// and no others.
//
// It is deliberately narrower than ValidateWithContext: the scope the statement
// is keyed on, and the username and address it sets. Validating a column the
// statement never touches refuses a value the write would have ignored, and the
// caller who then goes looking for something to put in that field is the caller
// who starts carrying a password hash around. The status is the live example —
// AdminWriter owns it, ProfileWriter cannot write it, and a handler assembling
// a User out of a profile form has no reason to name one.
//
// Both callers pass through here, so the address rule cannot drift between what
// registration accepts and what a profile save accepts. See SQLStore.UpdateUser.
func (u *User) validateProfile(ctx context.Context) error {
	if u == nil {
		return ErrNilUser
	}

	if err := u.Scope.Validate(); err != nil {
		return err
	}

	return validation.ValidateStructWithContext(ctx, u,
		validation.Field(&u.Username, validation.Required),
		validation.Field(&u.EmailAddress, validation.Required, emailAddressRule),
	)
}

// Redacted returns a copy of the user with every credential field cleared.
//
// It exists because the json:"-" tags on those fields bind encoding/json and
// nothing else — this module's own encoding package is codec-agnostic by
// design, and a User handed to a msgpack or CBOR encoder carries the password
// hash out with it. A response body, a cache entry, an event payload, and a log
// field should all carry the value this returns.
//
// The role slice is cloned rather than shared, so a caller mutating the
// redacted copy's roles cannot reach through to the value it came from.
//
// A nil User redacts to nil.
func (u *User) Redacted() *User {
	if u == nil {
		return nil
	}

	clone := *u
	clone.ServiceRoles = slices.Clone(u.ServiceRoles)
	clone.HashedPassword = ""
	clone.TwoFactorSecret = ""
	clone.EmailAddressVerificationToken = ""

	return &clone
}

// HasPassword reports whether the user holds a password credential at all.
//
// It is the question a password sign-in has to ask before it compares. A
// passwordless user's hash is the empty string, and an engine handed one has no
// way to know it was never set — so the refusal belongs here, at the flow that
// knows, rather than in whatever argon2 decides an empty hash compares equal
// to. Refusing a password sign-in for a user who has no password is also not
// the same answer as "wrong password": somebody who registered with a passkey
// should be sent back to their passkey.
//
// Note that it reads the field, so it answers about a user that was read
// unredacted. [User.Redacted] clears the hash, so a redacted user reports
// false — which is why this is a sign-in flow's question and not a profile
// page's.
func (u *User) HasPassword() bool {
	return u != nil && u.HashedPassword != ""
}

// TwoFactorEnabled reports whether the user has a second factor that has
// actually been proven, which is the question a sign-in flow means to ask.
func (u *User) TwoFactorEnabled() bool {
	return u != nil && u.TwoFactorSecret != "" && u.TwoFactorSecretVerifiedAt != nil
}

// EmailAddressVerified reports whether the user's address has been proven
// reachable.
func (u *User) EmailAddressVerified() bool {
	return u != nil && u.EmailAddressVerifiedAt != nil
}

// Archived reports whether the user has been soft-deleted.
func (u *User) Archived() bool {
	return u != nil && u.ArchivedAt != nil
}
