package billing

import (
	"strings"
	"time"

	"github.com/primandproper/primitives-go/capitalism"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"
)

const (
	// serviceName scopes this package's spans, logger, and instruments.
	serviceName = "billing"

	scopeKey        = serviceName + ".scope"
	accountKey      = serviceName + ".account"
	productKey      = serviceName + ".product"
	subscriptionKey = serviceName + ".subscription"
	purchaseKey     = serviceName + ".purchase"
	transactionKey  = serviceName + ".transaction"
	statusKey       = serviceName + ".status"
	countKey        = serviceName + ".count"
)

// currencyLength is how many characters an ISO 4217 alphabetic code has. It is
// the whole of what this package checks about a currency: the codes themselves
// are a registry that changes, and a library shipping a copy of it would be
// refusing a currency somebody can genuinely charge in.
const currencyLength = 3

// Kind says how a product is sold.
//
// It is a closed set, because it decides which other columns have to be there:
// a recurring product without a billing interval is a subscription nothing
// knows when to renew, and that check has to switch on something it can
// enumerate.
type Kind string

const (
	// KindRecurring is a product sold as a subscription, billed every
	// [Product.BillingIntervalMonths] months.
	KindRecurring Kind = "recurring"

	// KindOneTime is a product sold once and owned afterwards.
	KindOneTime Kind = "one_time"
)

// Valid reports whether k is one of the two kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindRecurring, KindOneTime:
		return true
	default:
		return false
	}
}

// String renders the kind as it is stored.
func (k Kind) String() string { return string(k) }

// TransactionStatus is what became of one attempt to move money.
//
// It is this package's own rather than capitalism's, because capitalism has no
// counterpart: [capitalism.SubscriptionStatus] is where an agreement stands,
// which is a different question from whether one charge succeeded. The four
// values are the ones every processor's payment vocabulary reduces to, and an
// adapter that cannot place a provider's word should record what it can rather
// than guess between Failed and Refunded, which are the two a reconciliation
// tells apart by their sign.
type TransactionStatus string

const (
	// TransactionPending is an attempt the processor has accepted and not yet
	// settled. It is where a transaction written from a payment intent starts.
	TransactionPending TransactionStatus = "pending"

	// TransactionSucceeded is an attempt that moved the money.
	TransactionSucceeded TransactionStatus = "succeeded"

	// TransactionFailed is an attempt that did not, and will not be retried
	// under this id. A retry is a new attempt and a new row.
	TransactionFailed TransactionStatus = "failed"

	// TransactionRefunded is an attempt whose money was given back.
	//
	// A partial refund is its own transaction carrying the amount returned
	// rather than a rewrite of the original, which is why the amount on a row
	// is the attempt's own and not the product's price — see billing/migrations.
	TransactionRefunded TransactionStatus = "refunded"
)

// Valid reports whether s is one of the four statuses.
//
// It is exported for the caller decoding one out of a request or a provider
// payload, which is the only place a status this package does not implement can
// come from: the store writes the column from a value of this type.
func (s TransactionStatus) Valid() bool {
	switch s {
	case TransactionPending, TransactionSucceeded, TransactionFailed, TransactionRefunded:
		return true
	default:
		return false
	}
}

// String renders the status as it is stored.
func (s TransactionStatus) String() string { return string(s) }

// Product is something a deployment sells.
//
// The catalog is scope-wide and carries no account, which is what separates a
// product from the two things that reference it: a product is a thing on offer,
// and who bought it is a [Subscription] or a [Purchase].
type Product struct {
	_ struct{} `json:"-"`

	// CreatedAt is when the product was added to the catalog. It is the
	// database's clock rather than the application's, read back by the write —
	// see billing/migrations.
	CreatedAt time.Time `json:"createdAt"`

	// LastUpdatedAt is when the product last changed, or nil for one that has
	// not been edited.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when the product was withdrawn from sale. An archived
	// product is excluded from every read that does not ask for archived rows,
	// and nothing stops the subscriptions already on it from renewing —
	// archiving a product takes it off the shelf, it does not cancel what has
	// been sold.
	ArchivedAt *time.Time `json:"archivedAt"`

	// ID identifies the product. Minted on write when empty.
	ID string `json:"id"`

	// Name is what the product is called. Required.
	Name string `json:"name"`

	// Description is prose about what is being sold.
	Description string `json:"description"`

	// Kind says how it is sold. Required, and one of the two [Kind] values.
	Kind Kind `json:"kind"`

	// Currency is the ISO 4217 alphabetic code AmountCents is denominated in,
	// upper-cased on write. Required.
	//
	// Only its length is checked. The code list is a registry that changes, and
	// a library shipping a copy of it would eventually be refusing a currency
	// somebody can genuinely charge in.
	Currency string `json:"currency"`

	// ExternalProductID is the payment provider's identifier for the same
	// product, or empty for one that was never mirrored to a provider — a free
	// tier, or a plan that only exists to be comped.
	//
	// Empty is stored as NULL, which is what lets the unique index over this
	// column mean something: every provider-backed product is unique within its
	// scope, and the ones with no provider behind them do not collide with each
	// other. See billing/migrations.
	ExternalProductID string `json:"externalProductID"`

	// Scope is whose catalog this is. See the tenancy package.
	Scope tenancy.Scope `json:"scope"`

	// AmountCents is the price in the currency's minor unit — cents for USD,
	// whole yen for JPY. It is int64 because a signed 32-bit count of cents runs
	// out at about twenty-one million dollars.
	AmountCents int64 `json:"amountCents"`

	// BillingIntervalMonths is how often a recurring product is billed, and 0
	// for a one-time one.
	//
	// Zero rather than a pointer because zero months is not a billing interval,
	// so the empty value cannot be confused with a real one. It is stored as
	// NULL. Required to be positive when Kind is [KindRecurring], and required
	// to be zero otherwise.
	BillingIntervalMonths int64 `json:"billingIntervalMonths"`
}

// Recurring reports whether the product is sold as a subscription.
func (p *Product) Recurring() bool { return p != nil && p.Kind == KindRecurring }

// normalize applies the writes this package makes to a caller's value before
// storing it. It is called by every write, on a copy.
func (p *Product) normalize() {
	p.Currency = NormalizeCurrency(p.Currency)
}

// validate reports whether the product is one this package can store.
func (p *Product) validate() error {
	switch {
	case p == nil:
		return ErrNilProduct
	case p.Name == "":
		return ErrEmptyProductName
	case !p.Kind.Valid():
		return platformerrors.Wrapf(ErrInvalidKind, "%q", p.Kind)
	case len(p.Currency) != currencyLength:
		return platformerrors.Wrapf(ErrInvalidCurrency, "%q", p.Currency)
	case p.AmountCents < 0:
		return ErrNegativeAmount
	case p.Recurring() && p.BillingIntervalMonths <= 0:
		return ErrEmptyBillingInterval
	case !p.Recurring() && p.BillingIntervalMonths != 0:
		return ErrUnexpectedBillingInterval
	default:
		return nil
	}
}

// Subscription is a recurring agreement: one account, one product, for as long
// as it is paid.
//
// Most of what it holds is a restatement of a fact the payment provider owns.
// That is the point: deciding whether an account is entitled has to be a read of
// one row rather than a call to the provider on a request path, which is a
// latency budget spent on a fact that changes a few times a year and an outage
// that would take the product down rather than the billing.
type Subscription struct {
	_ struct{} `json:"-"`

	// CreatedAt is when this row was written, which is not when the agreement
	// began at the provider — that is CurrentPeriodStart of its first period.
	CreatedAt time.Time `json:"createdAt"`

	// CurrentPeriodStart and CurrentPeriodEnd are the window the provider says
	// is currently paid for. Both required.
	CurrentPeriodStart time.Time `json:"currentPeriodStart"`
	CurrentPeriodEnd   time.Time `json:"currentPeriodEnd"`

	// LastUpdatedAt is when the row last changed, or nil for one nobody has
	// synced since it was written.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when the subscription was retired administratively. It is
	// not a cancellation: a cancelled subscription is one whose Status says so,
	// and it stays readable because the ledger rows against it still point at
	// it.
	ArchivedAt *time.Time `json:"archivedAt"`

	// ID identifies the subscription. Minted on write when empty.
	ID string `json:"id"`

	// BelongsToAccount is whose subscription this is. Required, and immutable
	// once written — moving one between accounts is not an edit, it is a
	// cancellation and a new agreement.
	BelongsToAccount string `json:"belongsToAccount"`

	// ProductID is what was subscribed to. Required, and mutable, because an
	// upgrade is the same agreement pointed at a different product.
	ProductID string `json:"productID"`

	// ExternalSubscriptionID is the provider's identifier for the same
	// agreement, or empty for one granted by hand — which is what
	// grandfathering somebody looks like. Empty is stored as NULL; see
	// [Product.ExternalProductID] for why that is what makes the uniqueness
	// mean something.
	ExternalSubscriptionID string `json:"externalSubscriptionID"`

	// Status is where the agreement stands with the processor, in capitalism's
	// vocabulary rather than a provider's words.
	//
	// What it means — which of these values leaves an account entitled — is
	// deliberately not decided here. That is policy, it differs between
	// deployments selling the same thing, and capitalism's documentation is
	// where the ruling lives. This field is the fact; the reading is yours.
	Status capitalism.SubscriptionStatus `json:"status"`

	// Scope is whose subscription this is.
	Scope tenancy.Scope `json:"scope"`
}

// CurrentAt reports whether the subscription's paid period covers t: not
// archived, started, and not yet ended.
//
// It is the same question [SubscriptionStore.ListCurrentSubscriptions] pages by,
// spelled once here so a caller holding a row and the store selecting rows
// cannot disagree about the boundary. The boundary is exclusive at the end — a
// subscription whose period ends exactly at t is over — which is the reading
// that leaves no instant at which one is neither current nor lapsed.
//
// It says nothing about the status. A past_due subscription inside its paid
// period is current by this reading and may well not be entitled, which is the
// policy this package deliberately does not hold.
func (s *Subscription) CurrentAt(t time.Time) bool {
	return s != nil &&
		s.ArchivedAt == nil &&
		!s.CurrentPeriodStart.After(t) &&
		s.CurrentPeriodEnd.After(t)
}

// validate reports whether the subscription is one this package can store.
func (s *Subscription) validate() error {
	switch {
	case s == nil:
		return ErrNilSubscription
	case s.BelongsToAccount == "":
		return ErrEmptyAccount
	case s.ProductID == "":
		return ErrEmptyProduct
	case !s.Status.Known():
		return platformerrors.Wrapf(ErrInvalidStatus, "%q", s.Status)
	case s.CurrentPeriodStart.IsZero() || s.CurrentPeriodEnd.IsZero():
		return ErrEmptyPeriod
	case !s.CurrentPeriodEnd.After(s.CurrentPeriodStart):
		return ErrBackwardsPeriod
	default:
		return nil
	}
}

// Purchase is a one-time sale: bought once, owned afterwards.
type Purchase struct {
	_ struct{} `json:"-"`

	// CreatedAt is when the sale was started, which is before the money moved.
	CreatedAt time.Time `json:"createdAt"`

	// CompletedAt is when the payment behind the purchase succeeded, or nil for
	// one still outstanding. It is the whole lifecycle this type has, and
	// [PurchaseStore.Complete] is the only thing that writes it.
	CompletedAt *time.Time `json:"completedAt"`

	// LastUpdatedAt is when the row last changed, or nil for one nothing has
	// touched since it was written.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when the purchase was retired administratively.
	ArchivedAt *time.Time `json:"archivedAt"`

	// ID identifies the purchase. Minted on write when empty.
	ID string `json:"id"`

	// BelongsToAccount is who bought it. Required.
	BelongsToAccount string `json:"belongsToAccount"`

	// ProductID is what was bought. Required.
	ProductID string `json:"productID"`

	// ExternalTransactionID is the provider's identifier for the payment, or
	// empty for a purchase granted without one. Empty is stored as NULL.
	ExternalTransactionID string `json:"externalTransactionID"`

	// Currency is the ISO 4217 code AmountCents is in, upper-cased on write.
	Currency string `json:"currency"`

	// Scope is whose purchase this is.
	Scope tenancy.Scope `json:"scope"`

	// AmountCents is what was actually charged, in the currency's minor unit.
	//
	// It is restated here rather than read through ProductID because a price is
	// a fact about the moment of sale: repricing a product must not rewrite what
	// somebody already paid.
	AmountCents int64 `json:"amountCents"`
}

// Complete reports whether the money for this purchase arrived.
func (p *Purchase) Complete() bool { return p != nil && p.CompletedAt != nil }

// normalize applies the writes this package makes to a caller's value before
// storing it.
func (p *Purchase) normalize() {
	p.Currency = NormalizeCurrency(p.Currency)
}

// validate reports whether the purchase is one this package can store.
func (p *Purchase) validate() error {
	switch {
	case p == nil:
		return ErrNilPurchase
	case p.BelongsToAccount == "":
		return ErrEmptyAccount
	case p.ProductID == "":
		return ErrEmptyProduct
	case len(p.Currency) != currencyLength:
		return platformerrors.Wrapf(ErrInvalidCurrency, "%q", p.Currency)
	case p.AmountCents < 0:
		return ErrNegativeAmount
	default:
		return nil
	}
}

// Transaction is what one attempt to move money left behind.
type Transaction struct {
	_ struct{} `json:"-"`

	// CreatedAt is when the attempt was recorded, which is also the order the
	// ledger walks in — the id sorts by creation time, and every page here walks
	// it.
	CreatedAt time.Time `json:"createdAt"`

	// LastUpdatedAt is when the row last changed, which for this table means
	// when its status last moved.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`

	// ArchivedAt is when the row was retired administratively.
	ArchivedAt *time.Time `json:"archivedAt"`

	// ID identifies the transaction. Minted on write when empty.
	ID string `json:"id"`

	// BelongsToAccount is whose money moved. Required.
	BelongsToAccount string `json:"belongsToAccount"`

	// SubscriptionID is the agreement this attempt renewed, or empty.
	//
	// At most one of SubscriptionID and PurchaseID is set: an attempt is either
	// a subscription's renewal or a purchase's payment. Both empty is legal and
	// means neither is still here, which is what a refund of something since
	// removed looks like.
	SubscriptionID string `json:"subscriptionID"`

	// PurchaseID is the sale this attempt paid for, or empty. See
	// SubscriptionID.
	PurchaseID string `json:"purchaseID"`

	// ExternalTransactionID is the provider's identifier for the attempt, or
	// empty. Empty is stored as NULL.
	//
	// This is the column that makes the ledger safe to write from a webhook.
	// Payment providers redeliver, and the unique index over it is what turns a
	// second delivery into [ErrTransactionExists] instead of a second row — see
	// billing/migrations.
	ExternalTransactionID string `json:"externalTransactionID"`

	// Status is what became of the attempt.
	Status TransactionStatus `json:"status"`

	// Currency is the ISO 4217 code AmountCents is in, upper-cased on write.
	Currency string `json:"currency"`

	// Scope is whose ledger this row is in.
	Scope tenancy.Scope `json:"scope"`

	// AmountCents is the amount this attempt moved, in the currency's minor
	// unit — which for a partial refund is not the price of anything.
	AmountCents int64 `json:"amountCents"`
}

// normalize applies the writes this package makes to a caller's value before
// storing it.
func (t *Transaction) normalize() {
	t.Currency = NormalizeCurrency(t.Currency)
}

// validate reports whether the transaction is one this package can store.
func (t *Transaction) validate() error {
	switch {
	case t == nil:
		return ErrNilTransaction
	case t.BelongsToAccount == "":
		return ErrEmptyAccount
	case !t.Status.Valid():
		return platformerrors.Wrapf(ErrInvalidStatus, "%q", t.Status)
	case len(t.Currency) != currencyLength:
		return platformerrors.Wrapf(ErrInvalidCurrency, "%q", t.Currency)
	case t.AmountCents < 0:
		return ErrNegativeAmount
	case t.SubscriptionID != "" && t.PurchaseID != "":
		return ErrAmbiguousTransaction
	default:
		return nil
	}
}

// NormalizeCurrency renders a currency code as it is stored: trimmed of
// surrounding space and upper-cased.
//
// It is exported because it is what a caller needs to reproduce a stored value,
// and because it is the answer to "why does my currency not match". ISO 4217
// codes are upper case, "usd" and "USD" are one currency, and a ledger that held
// both would be a ledger whose totals depend on which handler wrote each row.
func NormalizeCurrency(currency string) string {
	return strings.ToUpper(strings.TrimSpace(currency))
}

// requireID reports an invalid id for the empty string, which is the one input
// every keyed method here shares.
func requireID(id string) error {
	if id == "" {
		return platformerrors.ErrInvalidIDProvided
	}

	return nil
}

// requireAccount reports an empty account, which every account-keyed read
// refuses rather than answering with somebody else's page.
func requireAccount(accountID string) error {
	if accountID == "" {
		return ErrEmptyAccount
	}

	return nil
}
