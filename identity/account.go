package identity

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// BillingStatus is where an account stands with the payment processor.
//
// This package neither talks to a processor nor decides what a status implies —
// whether a trial account may still write is the application's rule. It stores
// the answer so that the rule has one place to read it from, and so that the
// question "which accounts lapsed" is an indexed query rather than a call out
// to a vendor.
//
// The four values here are not what a processor reports. That vocabulary is
// capitalism.SubscriptionStatus, which a webhook arrives carrying: each payments
// adapter maps its provider's words onto it — capitalism/stripe holds the Stripe
// table — so no consumer decodes provider JSON to learn a customer's standing.
// The step from one of those statuses to one of these is the application's,
// because it is policy rather than translation: whether trialing counts as paid,
// and whether past_due is still entitled, are the rules this package stores the
// answer to rather than derives. BillingSuspended has no counterpart at all — it
// is an operator action no processor reports.
type BillingStatus string

const (
	// BillingUnpaid is an account with no successful payment. It is the status
	// CreateAccount assigns when none is set.
	BillingUnpaid BillingStatus = "unpaid"

	// BillingTrial is an account inside a trial window.
	BillingTrial BillingStatus = "trial"

	// BillingPaid is an account in good standing with the processor.
	BillingPaid BillingStatus = "paid"

	// BillingSuspended is an account the operator has suspended for
	// non-payment. It is distinct from BillingUnpaid, which is a state an
	// account is born in — this one it was moved to.
	BillingSuspended BillingStatus = "suspended"
)

// Valid reports whether s is one of the four statuses.
func (s BillingStatus) Valid() bool {
	switch s {
	case BillingUnpaid, BillingTrial, BillingPaid, BillingSuspended:
		return true
	default:
		return false
	}
}

// String renders the status as it is stored.
func (s BillingStatus) String() string { return string(s) }

// BillingAddress is where an account's invoices go, in the field set a payment
// processor accepts.
//
// It is deliberately not a general-purpose postal address, and the name says so
// because the distinction is easy to lose. The decomposition here — two lines, a
// city, a state, a postal code, a country — is the one the processors impose on
// what they are sent, and storing something richer would only mean discarding
// the difference at the moment the address is handed over. Plenty of the world's
// addresses do not divide this way; an application that needs a person's address
// modeled honestly should keep its own, in its own table, and treat this as the
// lossy projection of it that billing requires.
//
// Every field is optional, and none is validated. There is no address format
// this module knows to be mandatory — not a postal code, not a state, not even a
// street line — so requiring any of them would reject real addresses to no end.
//
// It is a struct rather than seven fields spread across Account because it
// travels as a unit — a caller updating an address updates all of it — and
// because an application that never collects one leaves a zero value rather
// than seven empty strings. It is flattened into columns on the accounts table
// rather than being a table of its own, since an account has exactly one and
// the join would buy nothing.
type BillingAddress struct {
	_ struct{} `json:"-"`

	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postalCode"`
	Country    string `json:"country"`
	Phone      string `json:"phone"`
}

// Zero reports whether the address holds nothing, which is what an application
// that does not collect one leaves behind.
//
// A pointer receiver on a value type with no other methods, because the struct
// is seven strings and copying it to answer one comparison is the copy this
// check exists to avoid making at every call site.
func (a *BillingAddress) Zero() bool { return *a == BillingAddress{} }

// Account is an organization: the thing users belong to and invoices are
// addressed to.
//
// It carries a Scope like every other row here and is not itself one. See the
// package documentation for why that distinction is load-bearing.
type Account struct {
	_ struct{} `json:"-"`

	// CreatedAt is when the account was created, stamped by the Store.
	CreatedAt time.Time `json:"createdAt"`

	// LastUpdatedAt is when any field last changed, nil for an unedited account.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when the account was soft-deleted.
	ArchivedAt *time.Time `json:"archivedAt"`

	// SubscriptionPlanID names the plan the account is on, if the application
	// has plans. It is an opaque identifier because whose plan it is — the
	// processor's, a metering plan in this module's metering package, one of the
	// application's own — is not this package's business.
	SubscriptionPlanID *string `json:"subscriptionPlanID"`

	// LastPaymentProviderSyncedAt is when billing state was last reconciled with
	// the processor. It is what a reconciliation job orders by, so an account
	// that has never synced sorts first.
	LastPaymentProviderSyncedAt *time.Time `json:"lastPaymentProviderSyncedAt"`

	// BillingAddress is where invoices go. Optional.
	BillingAddress BillingAddress `json:"billingAddress"`

	// ID identifies the account.
	ID string `json:"id"`

	// Name is what the account is called, as its owner named it. It is not
	// unique: two unrelated organizations may both be called "Acme", and
	// enforcing otherwise makes registration fail for a reason the registrant
	// cannot act on.
	Name string `json:"name"`

	// OwnerUserID is the user who owns the account. It is a single user rather
	// than a set because ownership answers exactly one question — who is
	// ultimately responsible, and who cannot be removed — and a set makes that
	// question ambiguous at the moment it matters. Co-administrators are a
	// membership role.
	OwnerUserID string `json:"ownerUserID"`

	// BillingStatus is where the account stands with the processor.
	BillingStatus BillingStatus `json:"billingStatus"`

	// PaymentProcessorCustomerID is the account's identifier at the processor.
	PaymentProcessorCustomerID string `json:"paymentProcessorCustomerID"`

	// TimeZone is the IANA name of the zone this account's days are read in —
	// "America/Chicago", "Europe/Dublin" — and it is what a scheduled digest, a
	// rendered date, and a monthly billing boundary should all agree on.
	//
	// It lives on the account rather than on the user because the things that
	// need it are the account's: when the invoice period rolls over, when the
	// nightly job for this account runs. An application whose members are spread
	// across zones and wants each of them addressed in their own keeps that
	// beside the user, in its own table; this is the account's clock, not a
	// display preference.
	//
	// Empty means the account has not stated one, which is a legitimate answer
	// and the one a single-region application leaves here forever. Location is
	// what reads it. A non-empty value must load — see the package's validation
	// for what that costs on an image without zoneinfo.
	TimeZone string `json:"timeZone"`

	// Scope is whose directory this account is in.
	Scope tenancy.Scope `json:"scope"`
}

// EnsureDefaults fills an Account's optional fields.
func (a *Account) EnsureDefaults() {
	if a == nil {
		return
	}

	if a.BillingStatus == "" {
		a.BillingStatus = BillingUnpaid
	}
}

var _ validation.ValidatableWithContext = (*Account)(nil)

// ValidateWithContext checks an Account's invariants before it is written.
//
// The owner is required. An account with no owner is one whose every
// ownership-derived permission check resolves to nobody, and it is easier to
// refuse writing one than to explain the resulting authorization failures.
func (a *Account) ValidateWithContext(ctx context.Context) error {
	if a == nil {
		return ErrNilAccount
	}

	if err := a.Scope.Validate(); err != nil {
		return err
	}

	if !a.BillingStatus.Valid() {
		return platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "billing status %q", a.BillingStatus)
	}

	return validation.ValidateStructWithContext(ctx, a,
		validation.Field(&a.Name, validation.Required),
		validation.Field(&a.OwnerUserID, validation.Required),
		validation.Field(&a.TimeZone, timeZoneRule),
	)
}

// Location resolves TimeZone to a *time.Location, and reports UTC for an
// account that has not stated one.
//
// It exists so that "what does an empty TimeZone mean" is answered once. Every
// caller would otherwise write the LoadLocation call and the empty-string
// branch itself, and the branch is the half that gets it wrong — falling back
// to time.Local, which is the process's TZ variable, so the same account
// renders differently on two replicas of the same service.
//
// The error is the zoneinfo database's absence far more often than it is a bad
// name, since a stored value has already been validated on the way in. A caller
// that would rather degrade than fail can use the returned location regardless:
// it is never nil, and it is UTC whenever err is non-nil.
func (a *Account) Location() (*time.Location, error) {
	if a == nil || a.TimeZone == "" {
		return time.UTC, nil
	}

	loc, err := time.LoadLocation(a.TimeZone)
	if err != nil {
		return time.UTC, platformerrors.Wrapf(ErrInvalidTimeZone, "%q: %v", a.TimeZone, err)
	}

	return loc, nil
}

// Archived reports whether the account has been soft-deleted.
func (a *Account) Archived() bool { return a != nil && a.ArchivedAt != nil }
