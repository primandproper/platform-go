package billing

import (
	"context"
	"maps"
	"slices"
	"strconv"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/recording"
	"github.com/primandproper/platform-go/v15/webhooks"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The resource types this package's audit entries name. They are platform's
// vocabulary for platform's own tables, prefixed so a consumer's "product" is
// never mistaken for one of these.
const (
	// ResourceTypeProduct is what an audit entry about a Product names.
	ResourceTypeProduct = "billing.product"
	// ResourceTypeSubscription is what an audit entry about a Subscription names.
	ResourceTypeSubscription = "billing.subscription"
	// ResourceTypePurchase is what an audit entry about a Purchase names.
	ResourceTypePurchase = "billing.purchase"
	// ResourceTypeTransaction is what an audit entry about a Transaction names.
	ResourceTypeTransaction = "billing.transaction"
)

// The events this package's writes emit, one per write. They are platform's
// names for platform's own writes, which is what makes them constants here
// rather than strings a consumer mints.
//
// A subscriber may receive one only if the dispatcher's catalog knows it.
// [EventCatalog] is the fragment to merge into that catalog; an event type left
// out of it is still published to the outbox and dispatched to nobody.
const (
	// EventProductCreated says a product was added to the catalog.
	EventProductCreated webhooks.EventType = "billing.product.created"
	// EventProductUpdated says a product was rewritten; the payload names which
	// fields.
	EventProductUpdated webhooks.EventType = "billing.product.updated"
	// EventProductArchived says a product was withdrawn from sale.
	EventProductArchived webhooks.EventType = "billing.product.archived"

	// EventSubscriptionCreated says an agreement was opened.
	EventSubscriptionCreated webhooks.EventType = "billing.subscription.created"
	// EventSubscriptionUpdated says an agreement was rewritten, by a provider's
	// sync or by a status move; the payload names which fields, and carries the
	// status it moved from when it moved.
	EventSubscriptionUpdated webhooks.EventType = "billing.subscription.updated"
	// EventSubscriptionArchived says an agreement was retired administratively.
	// It is not a cancellation, which is an update to its status.
	EventSubscriptionArchived webhooks.EventType = "billing.subscription.archived"

	// EventPurchaseCreated says a sale was started, and is outstanding.
	EventPurchaseCreated webhooks.EventType = "billing.purchase.created"
	// EventPurchaseCompleted says the money for a sale arrived.
	EventPurchaseCompleted webhooks.EventType = "billing.purchase.completed"
	// EventPurchaseArchived says a sale was retired administratively.
	EventPurchaseArchived webhooks.EventType = "billing.purchase.archived"

	// EventTransactionRecorded says an attempt to move money was written to
	// the ledger.
	EventTransactionRecorded webhooks.EventType = "billing.transaction.recorded"
	// EventTransactionUpdated says an attempt's outcome moved; the payload
	// carries the status it moved from.
	EventTransactionUpdated webhooks.EventType = "billing.transaction.updated"
	// EventTransactionArchived says a ledger row was retired administratively.
	EventTransactionArchived webhooks.EventType = "billing.transaction.archived"
)

// EventCatalog is every event this package emits, described, for a consumer to
// merge into the catalog its dispatcher is built with:
//
//	catalog := webhooks.Catalog{OrderCreated: {Description: "..."}}
//	maps.Copy(catalog, billing.EventCatalog())
//
// It is a function rather than a package-level map so that no caller can
// mutate the one copy every other caller reads.
func EventCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		EventProductCreated:       {Description: "A product was added to the catalog."},
		EventProductUpdated:       {Description: "A product's details or price changed."},
		EventProductArchived:      {Description: "A product was withdrawn from sale."},
		EventSubscriptionCreated:  {Description: "A subscription was opened."},
		EventSubscriptionUpdated:  {Description: "A subscription's period, product or status changed."},
		EventSubscriptionArchived: {Description: "A subscription was retired by an operator."},
		EventPurchaseCreated:      {Description: "A one-time purchase was started."},
		EventPurchaseCompleted:    {Description: "The payment for a one-time purchase arrived."},
		EventPurchaseArchived:     {Description: "A one-time purchase was retired by an operator."},
		EventTransactionRecorded:  {Description: "A payment attempt was written to the ledger."},
		EventTransactionUpdated:   {Description: "A payment attempt's outcome changed."},
		EventTransactionArchived:  {Description: "A ledger row was retired by an operator."},
	}
}

// ProductEvent is the payload of every product event.
//
// It names the product and its price, which is what a subscriber told the
// catalog moved most often wants without a read, and for an update the names
// of the fields that moved.
type ProductEvent struct {
	_ struct{} `json:"-"`

	// ProductID is the product the event is about.
	ProductID string `json:"productID"`
	// Name is the product's name as the write left it.
	Name string `json:"name"`
	// Kind says how it is sold.
	Kind Kind `json:"kind"`
	// Currency is what AmountCents is denominated in.
	Currency string `json:"currency"`

	// Changed names the fields an update moved, sorted, and is empty for every
	// other event. The timestamp every save stamps is left off: a subscriber
	// asking what the operator edited is told nothing by it.
	Changed []string `json:"changed,omitempty"`

	// AmountCents is the price as the write left it.
	AmountCents int64 `json:"amountCents"`
}

// SubscriptionEvent is the payload of every subscription event.
type SubscriptionEvent struct {
	_ struct{} `json:"-"`

	// SubscriptionID is the agreement the event is about.
	SubscriptionID string `json:"subscriptionID"`
	// AccountID is whose agreement it is.
	AccountID string `json:"accountID"`
	// ProductID is what it is for, as the write left it.
	ProductID string `json:"productID"`
	// Status is where the agreement stands after the write.
	Status capitalism.SubscriptionStatus `json:"status"`
	// PreviousStatus is where it stood before, on an update that moved it, and
	// empty on every other event.
	PreviousStatus capitalism.SubscriptionStatus `json:"previousStatus,omitempty"`

	// Changed names the fields an update moved, sorted, and is empty for every
	// other event. See ProductEvent.Changed for what is left off.
	Changed []string `json:"changed,omitempty"`
}

// PurchaseEvent is the payload of every purchase event.
type PurchaseEvent struct {
	_ struct{} `json:"-"`

	// PurchaseID is the sale the event is about.
	PurchaseID string `json:"purchaseID"`
	// AccountID is who bought it.
	AccountID string `json:"accountID"`
	// ProductID is what was bought.
	ProductID string `json:"productID"`
	// Currency is what AmountCents is denominated in.
	Currency string `json:"currency"`
	// AmountCents is what was charged.
	AmountCents int64 `json:"amountCents"`
}

// TransactionEvent is the payload of every ledger event.
type TransactionEvent struct {
	_ struct{} `json:"-"`

	// TransactionID is the ledger row the event is about.
	TransactionID string `json:"transactionID"`
	// AccountID is whose money moved.
	AccountID string `json:"accountID"`
	// SubscriptionID is the agreement the attempt renewed, or empty.
	SubscriptionID string `json:"subscriptionID,omitempty"`
	// PurchaseID is the sale the attempt paid for, or empty.
	PurchaseID string `json:"purchaseID,omitempty"`
	// Status is what became of the attempt, as the write left it.
	Status TransactionStatus `json:"status"`
	// PreviousStatus is what it was before, on an update, and empty on every
	// other event.
	PreviousStatus TransactionStatus `json:"previousStatus,omitempty"`
	// Currency is what AmountCents is denominated in.
	Currency string `json:"currency"`
	// AmountCents is what the attempt moved.
	AmountCents int64 `json:"amountCents"`
}

// The metadata keys an audit entry here carries. They are read back by whoever
// reads the log.
const (
	metadataProductID      = "productID"
	metadataSubscriptionID = "subscriptionID"
	metadataPurchaseID     = "purchaseID"
	metadataStatus         = "status"
	metadataPreviousStatus = "previousStatus"
	metadataAmountCents    = "amountCents"
	metadataCurrency       = "currency"
)

// lastUpdatedAtField is the json name of the timestamp every update stamps,
// which a diff therefore always names and a changed-fields list should not.
const lastUpdatedAtField = "lastUpdatedAt"

// RecordingHooks is the Hooks a deployment that keeps an audit log and
// publishes events installs with WithHooks. Every write records an audit entry
// naming the row and emits the event above for it, both on the write's
// transaction, through the recording.Recorder it is built with.
//
// It implements Hooks outright rather than embedding NoopHooks, so a write
// added to Store later fails to compile here until somebody decides what it
// records. Embedded, the new write would compile and record nothing, which is
// the one failure an audit log cannot notice. That makes it the type a
// consumer embeds in turn: one that wants a single entry shaped differently
// overrides that method and inherits the rest.
//
// A subscription's, a purchase's and a ledger row's entries name the account
// they belong to as their SubjectID, so a Recorder whose ScopeResolver files by
// subject puts an account's billing history on the account's own chain. A
// product's names nobody: the catalog belongs to the scope it is sold in. As in
// waitlists, the account is not copied into an entry's metadata, where it would
// outlive an erasure of the account with nothing to report it.
//
// Nothing a hook is handed is a card number, a provider customer identifier or
// a secret: the rows carry the provider's identifiers for the product, the
// agreement and the payment, and those reach an update's diff because matching
// a provider's delivery to the row it wrote is what an investigation of a
// billing write does first. The events name rows by ID and amount and carry no
// provider identifier at all; a subscriber that needs one reads the row.
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

// AfterCreateProduct records a product being added to the catalog.
func (h *RecordingHooks) AfterCreateProduct(ctx context.Context, tx database.Tx, scope tenancy.Scope, product *Product) error {
	if product == nil {
		return ErrNilProduct
	}

	return h.recordProduct(ctx, tx, scope, product, audit.EventCreated, EventProductCreated, nil)
}

// AfterUpdateProduct records a product being rewritten. The audit entry carries
// the diff, old values and new; the event carries only the names of the fields
// that moved.
func (h *RecordingHooks) AfterUpdateProduct(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Product) error {
	if before == nil || after == nil {
		return ErrNilProduct
	}

	changes, err := audit.Diff(before, after)
	if err != nil {
		return platformerrors.Wrap(err, "diffing the updated billing product")
	}

	return h.recordProduct(ctx, tx, scope, after, audit.EventUpdated, EventProductUpdated, changes)
}

// AfterArchiveProduct records a product being withdrawn from sale.
func (h *RecordingHooks) AfterArchiveProduct(ctx context.Context, tx database.Tx, scope tenancy.Scope, product *Product) error {
	if product == nil {
		return ErrNilProduct
	}

	return h.recordProduct(ctx, tx, scope, product, audit.EventArchived, EventProductArchived, nil)
}

// AfterCreateSubscription records an agreement being opened.
func (h *RecordingHooks) AfterCreateSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subscription *Subscription,
) error {
	if subscription == nil {
		return ErrNilSubscription
	}

	return h.recordSubscription(ctx, tx, scope, nil, subscription, audit.EventCreated, EventSubscriptionCreated)
}

// AfterUpdateSubscription records a provider's sync rewriting an agreement. The
// entry carries the diff and is filed under the stored account's subject, which
// a sync cannot move.
func (h *RecordingHooks) AfterUpdateSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	before, after *Subscription,
) error {
	if before == nil || after == nil {
		return ErrNilSubscription
	}

	return h.recordSubscription(ctx, tx, scope, before, after, audit.EventUpdated, EventSubscriptionUpdated)
}

// AfterSetSubscriptionStatus records a status move as the same kind of update
// an edit is: both rows diffed, and the status it left and the one it is in
// carried in the entry's metadata and the event's payload.
func (h *RecordingHooks) AfterSetSubscriptionStatus(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	before, after *Subscription,
) error {
	if before == nil || after == nil {
		return ErrNilSubscription
	}

	return h.recordSubscription(ctx, tx, scope, before, after, audit.EventUpdated, EventSubscriptionUpdated)
}

// AfterArchiveSubscription records an agreement being retired administratively.
func (h *RecordingHooks) AfterArchiveSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subscription *Subscription,
) error {
	if subscription == nil {
		return ErrNilSubscription
	}

	return h.recordSubscription(ctx, tx, scope, nil, subscription, audit.EventArchived, EventSubscriptionArchived)
}

// AfterCreatePurchase records a sale being started.
func (h *RecordingHooks) AfterCreatePurchase(ctx context.Context, tx database.Tx, scope tenancy.Scope, purchase *Purchase) error {
	if purchase == nil {
		return ErrNilPurchase
	}

	return h.recordPurchase(ctx, tx, scope, purchase, audit.EventCreated, EventPurchaseCreated)
}

// AfterCompletePurchase records the money for a sale arriving. It is handed no
// row from before because the one before is implied, outstanding, so the entry
// carries no diff and the event's type is what says the purchase completed.
func (h *RecordingHooks) AfterCompletePurchase(ctx context.Context, tx database.Tx, scope tenancy.Scope, purchase *Purchase) error {
	if purchase == nil {
		return ErrNilPurchase
	}

	return h.recordPurchase(ctx, tx, scope, purchase, audit.EventUpdated, EventPurchaseCompleted)
}

// AfterArchivePurchase records a sale being retired administratively.
func (h *RecordingHooks) AfterArchivePurchase(ctx context.Context, tx database.Tx, scope tenancy.Scope, purchase *Purchase) error {
	if purchase == nil {
		return ErrNilPurchase
	}

	return h.recordPurchase(ctx, tx, scope, purchase, audit.EventArchived, EventPurchaseArchived)
}

// AfterRecordTransaction records an attempt to move money being written to the
// ledger.
func (h *RecordingHooks) AfterRecordTransaction(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	transaction *Transaction,
) error {
	if transaction == nil {
		return ErrNilTransaction
	}

	return h.recordTransaction(ctx, tx, scope, nil, transaction, audit.EventCreated, EventTransactionRecorded)
}

// AfterSetTransactionStatus records an attempt's outcome moving, and from what.
// A ledger row's status is the one field an investigation asks about, so the
// status it left is in the entry's metadata and the event's payload beside the
// diff.
func (h *RecordingHooks) AfterSetTransactionStatus(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	before, after *Transaction,
) error {
	if before == nil || after == nil {
		return ErrNilTransaction
	}

	return h.recordTransaction(ctx, tx, scope, before, after, audit.EventUpdated, EventTransactionUpdated)
}

// AfterArchiveTransaction records a ledger row being retired administratively.
func (h *RecordingHooks) AfterArchiveTransaction(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	transaction *Transaction,
) error {
	if transaction == nil {
		return ErrNilTransaction
	}

	return h.recordTransaction(ctx, tx, scope, nil, transaction, audit.EventArchived, EventTransactionArchived)
}

// recordProduct writes the entry and the event for a write to the catalog. The
// entry names no subject: a product belongs to the scope it is sold in.
func (h *RecordingHooks) recordProduct(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	product *Product,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
	changes map[string]audit.Change,
) error {
	entry := &recording.Entry{
		ResourceType: ResourceTypeProduct,
		ResourceID:   product.ID,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata:     amountMetadata(product.AmountCents, product.Currency),
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: product.ID,
		Payload: &ProductEvent{
			ProductID:   product.ID,
			Name:        product.Name,
			Kind:        product.Kind,
			Currency:    product.Currency,
			AmountCents: product.AmountCents,
			Changed:     changedFields(changes),
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// recordSubscription writes the entry and the event for a write to one
// agreement. before is nil for every write that is not an update; for one that
// is, the pair is diffed and a status that moved is recorded as having moved.
func (h *RecordingHooks) recordSubscription(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	before, after *Subscription,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
) error {
	var (
		changes  map[string]audit.Change
		previous capitalism.SubscriptionStatus
	)

	metadata := map[string]string{
		metadataProductID: after.ProductID,
		metadataStatus:    after.Status.String(),
	}

	if before != nil {
		var err error
		if changes, err = audit.Diff(before, after); err != nil {
			return platformerrors.Wrap(err, "diffing the updated billing subscription")
		}

		if before.Status != after.Status {
			previous = before.Status
			metadata[metadataPreviousStatus] = previous.String()
		}
	}

	entry := &recording.Entry{
		ResourceType: ResourceTypeSubscription,
		ResourceID:   after.ID,
		SubjectID:    after.BelongsToAccount,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata:     metadata,
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: after.ID,
		Payload: &SubscriptionEvent{
			SubscriptionID: after.ID,
			AccountID:      after.BelongsToAccount,
			ProductID:      after.ProductID,
			Status:         after.Status,
			PreviousStatus: previous,
			Changed:        changedFields(changes),
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// recordPurchase writes the entry and the event for a write to one sale. No
// purchase write is handed a pair, so no purchase entry carries a diff.
func (h *RecordingHooks) recordPurchase(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	purchase *Purchase,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
) error {
	metadata := amountMetadata(purchase.AmountCents, purchase.Currency)
	metadata[metadataProductID] = purchase.ProductID

	entry := &recording.Entry{
		ResourceType: ResourceTypePurchase,
		ResourceID:   purchase.ID,
		SubjectID:    purchase.BelongsToAccount,
		EventType:    auditEventType,
		Metadata:     metadata,
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: purchase.ID,
		Payload: &PurchaseEvent{
			PurchaseID:  purchase.ID,
			AccountID:   purchase.BelongsToAccount,
			ProductID:   purchase.ProductID,
			Currency:    purchase.Currency,
			AmountCents: purchase.AmountCents,
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// recordTransaction writes the entry and the event for a write to one ledger
// row. before is nil for every write that is not a status move.
func (h *RecordingHooks) recordTransaction(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	before, after *Transaction,
	auditEventType audit.EventType,
	eventType webhooks.EventType,
) error {
	var (
		changes  map[string]audit.Change
		previous TransactionStatus
	)

	metadata := amountMetadata(after.AmountCents, after.Currency)
	metadata[metadataStatus] = after.Status.String()

	if after.SubscriptionID != "" {
		metadata[metadataSubscriptionID] = after.SubscriptionID
	}

	if after.PurchaseID != "" {
		metadata[metadataPurchaseID] = after.PurchaseID
	}

	if before != nil {
		var err error
		if changes, err = audit.Diff(before, after); err != nil {
			return platformerrors.Wrap(err, "diffing the updated billing transaction")
		}

		previous = before.Status
		metadata[metadataPreviousStatus] = previous.String()
	}

	entry := &recording.Entry{
		ResourceType: ResourceTypeTransaction,
		ResourceID:   after.ID,
		SubjectID:    after.BelongsToAccount,
		EventType:    auditEventType,
		Changes:      changes,
		Metadata:     metadata,
	}

	event := &webhooks.Event{
		EventType:   eventType,
		OrderingKey: after.ID,
		Payload: &TransactionEvent{
			TransactionID:  after.ID,
			AccountID:      after.BelongsToAccount,
			SubscriptionID: after.SubscriptionID,
			PurchaseID:     after.PurchaseID,
			Status:         after.Status,
			PreviousStatus: previous,
			Currency:       after.Currency,
			AmountCents:    after.AmountCents,
		},
	}

	return h.recorder.Record(ctx, tx, scope, event, entry)
}

// amountMetadata is the metadata naming a price or a charge.
func amountMetadata(amountCents int64, currency string) map[string]string {
	return map[string]string{
		metadataAmountCents: strconv.FormatInt(amountCents, 10),
		metadataCurrency:    currency,
	}
}

// changedFields names the fields a diff says moved, sorted, without the
// timestamp every save stamps. It is nil for a nil diff, so an event for a
// write that is not an update carries no changed list at all.
func changedFields(changes map[string]audit.Change) []string {
	if len(changes) == 0 {
		return nil
	}

	fields := slices.Sorted(maps.Keys(changes))

	return slices.DeleteFunc(fields, func(field string) bool { return field == lastUpdatedAtField })
}
