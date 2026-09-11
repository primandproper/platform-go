// Package primandproper.platform.billing.v1 is the wire schema for what a
// deployment sells and what its customers paid: the catalog, the recurring
// agreements, the one-time sales, and the ledger of attempts.
//
// This file is shipped inside the published Go module, and it is the file
// itself that is shipped -- not a copy for you to keep in sync. A consumer puts
// the module's proto directories on protoc's path and imports this file by its
// canonical name, exactly as identity.proto and filtering.proto already work:
//
//	PLATFORM_PROTO := $(shell go list -m -f '{{.Dir}}' github.com/primandproper/platform-go/v14)
//
//	protoc --proto_path proto/ \
//	    --proto_path $(PLATFORM_PROTO)/billing/proto \
//	    --proto_path $(PLATFORM_PROTO)/filtering/proto \
//	    --go_opt=Mprimandproper/platform/billing/v1/billing.proto=github.com/primandproper/platform-go/v14/billing/billingpb \
//	    $(CONSUMER_PROTO_FILES)   # the platform files deliberately absent from that list
//
// Field numbers are the compatibility promise, across every language a consumer
// generates into. Numbers are never reused and never repurposed: a field that
// goes away is reserved.
//
// # The one thing this schema will not carry
//
// There is no is_active, entitled, in_good_standing or account standing of any
// kind, on any message here, and there never will be. billing stores facts a
// payment provider owns and interprets none of them; a computed standing is the
// interpretation, it differs between two deployments selling the same thing, and
// putting it on the wire would put every consumer's policy on this module's
// release cadence.
//
// What ships instead is [Subscription.status], which is the fact. A consumer
// reads it and decides. The seam that decision belongs in already exists --
// billing/plans fills entitlements' PlanSource with this store's
// current-subscription read plus a function the consumer writes -- so a
// GetAccountStanding RPC here would be a second home for a policy that already
// has one.
//
// # Why status is a string and the other two are enums
//
// [Product.kind] and [Transaction.status] are enums. Both are closed sets
// billing defines itself and validates every write against, so a value outside
// the enum is a row the store would have refused; the generated constant is
// exactly as complete as the column.
//
// [Subscription.status] is a string, and it is the one borrowed vocabulary in
// this file. The set is capitalism.SubscriptionStatus -- the closed, documented
// set every payment adapter maps its provider's words onto -- and it is defined
// in a different module. An enum here would put that vocabulary on this module's
// release cadence, which is the objection identity/grpc's pattern section raises
// against a generated enum for a catalog somebody else owns; worse, a status
// primitives-go added and billing stored would render as UNSPECIFIED until this
// file caught up, which is a real row reaching a client as "we don't know".
// The wire value is the documented string -- "active", "past_due",
// "incomplete_expired" -- and it is never empty on a stored row, because the
// empty string is capitalism's unknown and billing refuses to store it.
//
// # What is not here, and why
//
// No scope field, anywhere. A scope a client could name is a cross-tenant read
// hiding behind a request field; it comes off the principal the consumer's
// interceptor put on the context. See identity.proto, which says this at
// greater length.
//
// No write that a payment processor's callback makes. Seven of billing's thirty
// store methods are absent from this service: CreateSubscription,
// UpdateSubscription, SetSubscriptionStatus, CreatePurchase, CompletePurchase,
// RecordTransaction and SetTransactionStatus. Their caller is not a client. It
// is a Stripe or RevenueCat receiver the consumer owns, or the checkout handler
// that created the payment intent, and both are already inside a transaction
// that is writing an audit entry and an outbox event beside the billing row. An
// RPC moves that write out of the transaction that was the entire point of it.
// billing/grpc/doc.go carries the ruling and billing.Store documents it on each
// method.
//
// No lookup by a payment provider's identifier. billing.Store has four --
// GetProductByExternalID and its three siblings -- and none is an RPC. The
// caller holding a provider's identifier is the callback that was handed one,
// and it already holds the store; a client that could ask "whose subscription is
// sub_1234" would be reading the provider's namespace rather than its own rows,
// against an id space it did not mint. A response about a row you may already
// read still carries the provider's id for it, which is the opposite direction
// and is how a console links out to the processor.
//
// No amount on any write. The only thing that legitimately changes about a
// ledger row is its status and the only thing that changes about a purchase is
// whether the money arrived, so there is no statement able to assign either --
// see billing's own documentation on why a price is a fact about a moment.

// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.11
// 	protoc        v6.33.1
// source: primandproper/platform/billing/v1/billing.proto

package billingpb

import (
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"

	filteringpb "github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// ProductKind says how a product is sold. It is billing's own closed set, and
// it decides which other fields have to be there: a recurring product without a
// billing interval is a subscription nothing knows when to renew.
type ProductKind int32

const (
	// PRODUCT_KIND_UNSPECIFIED is a request that named no kind. It is not a
	// stored value -- the store refuses a product whose kind is neither of the
	// two below.
	ProductKind_PRODUCT_KIND_UNSPECIFIED ProductKind = 0
	// PRODUCT_KIND_RECURRING is sold as a subscription, billed every
	// billing_interval_months months.
	ProductKind_PRODUCT_KIND_RECURRING ProductKind = 1
	// PRODUCT_KIND_ONE_TIME is sold once and owned afterwards. It carries no
	// billing interval, and one that does is refused.
	ProductKind_PRODUCT_KIND_ONE_TIME ProductKind = 2
)

// Enum value maps for ProductKind.
var (
	ProductKind_name = map[int32]string{
		0: "PRODUCT_KIND_UNSPECIFIED",
		1: "PRODUCT_KIND_RECURRING",
		2: "PRODUCT_KIND_ONE_TIME",
	}
	ProductKind_value = map[string]int32{
		"PRODUCT_KIND_UNSPECIFIED": 0,
		"PRODUCT_KIND_RECURRING":   1,
		"PRODUCT_KIND_ONE_TIME":    2,
	}
)

func (x ProductKind) Enum() *ProductKind {
	p := new(ProductKind)
	*p = x
	return p
}

func (x ProductKind) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ProductKind) Descriptor() protoreflect.EnumDescriptor {
	return file_primandproper_platform_billing_v1_billing_proto_enumTypes[0].Descriptor()
}

func (ProductKind) Type() protoreflect.EnumType {
	return &file_primandproper_platform_billing_v1_billing_proto_enumTypes[0]
}

func (x ProductKind) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ProductKind.Descriptor instead.
func (ProductKind) EnumDescriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{0}
}

// TransactionStatus is what became of one attempt to move money. It is
// billing's own closed set rather than a payment provider's: where an agreement
// stands is a different question from whether one charge succeeded, and
// Subscription.status is the other one.
type TransactionStatus int32

const (
	// TRANSACTION_STATUS_UNSPECIFIED is not a stored value. Every ledger row
	// carries one of the four below.
	TransactionStatus_TRANSACTION_STATUS_UNSPECIFIED TransactionStatus = 0
	// TRANSACTION_STATUS_PENDING is an attempt the processor has accepted and
	// not yet settled.
	TransactionStatus_TRANSACTION_STATUS_PENDING TransactionStatus = 1
	// TRANSACTION_STATUS_SUCCEEDED is an attempt that moved the money.
	TransactionStatus_TRANSACTION_STATUS_SUCCEEDED TransactionStatus = 2
	// TRANSACTION_STATUS_FAILED is an attempt that did not, and will not be
	// retried under this id. A retry is a new attempt and a new row.
	TransactionStatus_TRANSACTION_STATUS_FAILED TransactionStatus = 3
	// TRANSACTION_STATUS_REFUNDED is an attempt whose money was given back. A
	// partial refund is its own row carrying the amount returned, which is why
	// an amount here is not the price of anything.
	TransactionStatus_TRANSACTION_STATUS_REFUNDED TransactionStatus = 4
)

// Enum value maps for TransactionStatus.
var (
	TransactionStatus_name = map[int32]string{
		0: "TRANSACTION_STATUS_UNSPECIFIED",
		1: "TRANSACTION_STATUS_PENDING",
		2: "TRANSACTION_STATUS_SUCCEEDED",
		3: "TRANSACTION_STATUS_FAILED",
		4: "TRANSACTION_STATUS_REFUNDED",
	}
	TransactionStatus_value = map[string]int32{
		"TRANSACTION_STATUS_UNSPECIFIED": 0,
		"TRANSACTION_STATUS_PENDING":     1,
		"TRANSACTION_STATUS_SUCCEEDED":   2,
		"TRANSACTION_STATUS_FAILED":      3,
		"TRANSACTION_STATUS_REFUNDED":    4,
	}
)

func (x TransactionStatus) Enum() *TransactionStatus {
	p := new(TransactionStatus)
	*p = x
	return p
}

func (x TransactionStatus) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (TransactionStatus) Descriptor() protoreflect.EnumDescriptor {
	return file_primandproper_platform_billing_v1_billing_proto_enumTypes[1].Descriptor()
}

func (TransactionStatus) Type() protoreflect.EnumType {
	return &file_primandproper_platform_billing_v1_billing_proto_enumTypes[1]
}

func (x TransactionStatus) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use TransactionStatus.Descriptor instead.
func (TransactionStatus) EnumDescriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{1}
}

// Product is something a deployment sells, as a client sees it.
//
// The catalog is scope-wide and carries no account: a product is a thing on
// offer, and who bought it is a [Subscription] or a [Purchase].
type Product struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// created_at is when the product was added to the catalog, assigned by the
	// database.
	CreatedAt *timestamppb.Timestamp `protobuf:"bytes,1,opt,name=created_at,json=createdAt,proto3" json:"created_at,omitempty"`
	// last_updated_at is when it was last revised, unset for one nobody has
	// edited.
	LastUpdatedAt *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=last_updated_at,json=lastUpdatedAt,proto3" json:"last_updated_at,omitempty"`
	// archived_at is when it was withdrawn from sale, unset while it is on the
	// shelf. Withdrawn rather than deleted: the subscriptions already on it keep
	// renewing and the purchases already made stay readable.
	ArchivedAt *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=archived_at,json=archivedAt,proto3" json:"archived_at,omitempty"`
	// id is the row, and what every other RPC here names a product by.
	Id string `protobuf:"bytes,4,opt,name=id,proto3" json:"id,omitempty"`
	// name is what the product is called, and description is prose about what is
	// being sold. Both are text somebody typed -- render them, never trust them.
	Name        string `protobuf:"bytes,5,opt,name=name,proto3" json:"name,omitempty"`
	Description string `protobuf:"bytes,6,opt,name=description,proto3" json:"description,omitempty"`
	// kind says how it is sold.
	Kind ProductKind `protobuf:"varint,7,opt,name=kind,proto3,enum=primandproper.platform.billing.v1.ProductKind" json:"kind,omitempty"`
	// currency is the ISO 4217 alphabetic code amount_cents is denominated in,
	// upper case. Only its length is checked on write: the code list is a
	// registry that changes, and a library shipping a copy of it would eventually
	// refuse a currency somebody can genuinely charge in.
	Currency string `protobuf:"bytes,8,opt,name=currency,proto3" json:"currency,omitempty"`
	// external_product_id is the payment provider's identifier for the same
	// product, empty for one that was never mirrored to a provider -- a free
	// tier, or a plan that only exists to be comped.
	ExternalProductId string `protobuf:"bytes,9,opt,name=external_product_id,json=externalProductID,proto3" json:"external_product_id,omitempty"`
	// amount_cents is the price in the currency's minor unit: cents for USD,
	// whole yen for JPY. It is 64-bit because a signed 32-bit count of cents runs
	// out at about twenty-one million dollars.
	AmountCents int64 `protobuf:"varint,10,opt,name=amount_cents,json=amountCents,proto3" json:"amount_cents,omitempty"`
	// billing_interval_months is how often a recurring product is billed, and 0
	// for a one-time one. Required to be positive when kind is
	// PRODUCT_KIND_RECURRING and required to be zero otherwise.
	BillingIntervalMonths int64 `protobuf:"varint,11,opt,name=billing_interval_months,json=billingIntervalMonths,proto3" json:"billing_interval_months,omitempty"`
	unknownFields         protoimpl.UnknownFields
	sizeCache             protoimpl.SizeCache
}

func (x *Product) Reset() {
	*x = Product{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Product) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Product) ProtoMessage() {}

func (x *Product) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Product.ProtoReflect.Descriptor instead.
func (*Product) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{0}
}

func (x *Product) GetCreatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.CreatedAt
	}
	return nil
}

func (x *Product) GetLastUpdatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.LastUpdatedAt
	}
	return nil
}

func (x *Product) GetArchivedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ArchivedAt
	}
	return nil
}

func (x *Product) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *Product) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *Product) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

func (x *Product) GetKind() ProductKind {
	if x != nil {
		return x.Kind
	}
	return ProductKind_PRODUCT_KIND_UNSPECIFIED
}

func (x *Product) GetCurrency() string {
	if x != nil {
		return x.Currency
	}
	return ""
}

func (x *Product) GetExternalProductId() string {
	if x != nil {
		return x.ExternalProductId
	}
	return ""
}

func (x *Product) GetAmountCents() int64 {
	if x != nil {
		return x.AmountCents
	}
	return 0
}

func (x *Product) GetBillingIntervalMonths() int64 {
	if x != nil {
		return x.BillingIntervalMonths
	}
	return 0
}

// Subscription is a recurring agreement: one account, one product, for as long
// as it is paid.
//
// Most of what it holds is a restatement of a fact the payment provider owns.
// That is the point: deciding whether an account is entitled has to be a read of
// one row rather than a call to the provider on a request path.
type Subscription struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// created_at is when this row was written, which is not when the agreement
	// began at the provider -- that is current_period_start of its first period.
	CreatedAt *timestamppb.Timestamp `protobuf:"bytes,1,opt,name=created_at,json=createdAt,proto3" json:"created_at,omitempty"`
	// current_period_start and current_period_end are the window the provider
	// says is currently paid for. The end is exclusive: a subscription whose
	// period ends exactly now is over, which is the reading that leaves no
	// instant at which one is neither current nor lapsed.
	CurrentPeriodStart *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=current_period_start,json=currentPeriodStart,proto3" json:"current_period_start,omitempty"`
	CurrentPeriodEnd   *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=current_period_end,json=currentPeriodEnd,proto3" json:"current_period_end,omitempty"`
	// last_updated_at is when the row last changed, unset for one nothing has
	// synced since it was written.
	LastUpdatedAt *timestamppb.Timestamp `protobuf:"bytes,4,opt,name=last_updated_at,json=lastUpdatedAt,proto3" json:"last_updated_at,omitempty"`
	// archived_at is when the subscription was retired administratively. It is
	// not a cancellation -- a cancelled subscription is one whose status says so.
	ArchivedAt *timestamppb.Timestamp `protobuf:"bytes,5,opt,name=archived_at,json=archivedAt,proto3" json:"archived_at,omitempty"`
	// id is the row.
	Id string `protobuf:"bytes,6,opt,name=id,proto3" json:"id,omitempty"`
	// belongs_to_account is whose subscription this is. No request here carries it
	// as a way of writing one, and the account-keyed reads name an account the
	// caller has to be permitted against.
	BelongsToAccount string `protobuf:"bytes,7,opt,name=belongs_to_account,json=belongsToAccount,proto3" json:"belongs_to_account,omitempty"`
	// product_id is what was subscribed to. It is mutable at the store, because
	// an upgrade is the same agreement pointed at a different product.
	ProductId string `protobuf:"bytes,8,opt,name=product_id,json=productID,proto3" json:"product_id,omitempty"`
	// external_subscription_id is the provider's identifier for the same
	// agreement, empty for one granted by hand -- which is what grandfathering
	// somebody looks like.
	ExternalSubscriptionId string `protobuf:"bytes,9,opt,name=external_subscription_id,json=externalSubscriptionID,proto3" json:"external_subscription_id,omitempty"`
	// status is where the agreement stands with the processor, in capitalism's
	// vocabulary rather than a provider's words: "incomplete",
	// "incomplete_expired", "trialing", "active", "past_due", "canceled",
	// "unpaid", "paused".
	//
	// It is a string rather than a generated enum, and it is never empty on a
	// stored row. See this file's opening comment for both.
	//
	// What it means -- which of these leaves an account entitled -- is
	// deliberately not decided here, and there is no field on this message that
	// decides it. That is policy, it differs between deployments selling the same
	// thing, and this field is the fact the policy reads.
	Status        string `protobuf:"bytes,10,opt,name=status,proto3" json:"status,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Subscription) Reset() {
	*x = Subscription{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Subscription) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Subscription) ProtoMessage() {}

func (x *Subscription) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Subscription.ProtoReflect.Descriptor instead.
func (*Subscription) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{1}
}

func (x *Subscription) GetCreatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.CreatedAt
	}
	return nil
}

func (x *Subscription) GetCurrentPeriodStart() *timestamppb.Timestamp {
	if x != nil {
		return x.CurrentPeriodStart
	}
	return nil
}

func (x *Subscription) GetCurrentPeriodEnd() *timestamppb.Timestamp {
	if x != nil {
		return x.CurrentPeriodEnd
	}
	return nil
}

func (x *Subscription) GetLastUpdatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.LastUpdatedAt
	}
	return nil
}

func (x *Subscription) GetArchivedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ArchivedAt
	}
	return nil
}

func (x *Subscription) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *Subscription) GetBelongsToAccount() string {
	if x != nil {
		return x.BelongsToAccount
	}
	return ""
}

func (x *Subscription) GetProductId() string {
	if x != nil {
		return x.ProductId
	}
	return ""
}

func (x *Subscription) GetExternalSubscriptionId() string {
	if x != nil {
		return x.ExternalSubscriptionId
	}
	return ""
}

func (x *Subscription) GetStatus() string {
	if x != nil {
		return x.Status
	}
	return ""
}

// Purchase is a one-time sale: bought once, owned afterwards.
type Purchase struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// created_at is when the sale was started, which is before the money moved.
	CreatedAt *timestamppb.Timestamp `protobuf:"bytes,1,opt,name=created_at,json=createdAt,proto3" json:"created_at,omitempty"`
	// completed_at is when the payment behind the purchase succeeded, unset for
	// one still outstanding. It is the whole lifecycle this message has.
	CompletedAt *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=completed_at,json=completedAt,proto3" json:"completed_at,omitempty"`
	// last_updated_at is when the row last changed, unset for one nothing has
	// touched.
	LastUpdatedAt *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=last_updated_at,json=lastUpdatedAt,proto3" json:"last_updated_at,omitempty"`
	// archived_at is when the purchase was retired administratively. It is not a
	// refund -- a refund is a transaction of its own.
	ArchivedAt *timestamppb.Timestamp `protobuf:"bytes,4,opt,name=archived_at,json=archivedAt,proto3" json:"archived_at,omitempty"`
	// id is the row.
	Id string `protobuf:"bytes,5,opt,name=id,proto3" json:"id,omitempty"`
	// belongs_to_account is who bought it, and product_id is what they bought.
	BelongsToAccount string `protobuf:"bytes,6,opt,name=belongs_to_account,json=belongsToAccount,proto3" json:"belongs_to_account,omitempty"`
	ProductId        string `protobuf:"bytes,7,opt,name=product_id,json=productID,proto3" json:"product_id,omitempty"`
	// external_transaction_id is the provider's identifier for the payment, empty
	// for a purchase granted without one.
	ExternalTransactionId string `protobuf:"bytes,8,opt,name=external_transaction_id,json=externalTransactionID,proto3" json:"external_transaction_id,omitempty"`
	// currency is the ISO 4217 code amount_cents is in, upper case.
	Currency string `protobuf:"bytes,9,opt,name=currency,proto3" json:"currency,omitempty"`
	// amount_cents is what was actually charged, in the currency's minor unit. It
	// is restated on the sale rather than read through the product because a
	// price is a fact about the moment: repricing a product must not rewrite what
	// somebody already paid.
	AmountCents   int64 `protobuf:"varint,10,opt,name=amount_cents,json=amountCents,proto3" json:"amount_cents,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Purchase) Reset() {
	*x = Purchase{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Purchase) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Purchase) ProtoMessage() {}

func (x *Purchase) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Purchase.ProtoReflect.Descriptor instead.
func (*Purchase) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{2}
}

func (x *Purchase) GetCreatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.CreatedAt
	}
	return nil
}

func (x *Purchase) GetCompletedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.CompletedAt
	}
	return nil
}

func (x *Purchase) GetLastUpdatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.LastUpdatedAt
	}
	return nil
}

func (x *Purchase) GetArchivedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ArchivedAt
	}
	return nil
}

func (x *Purchase) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *Purchase) GetBelongsToAccount() string {
	if x != nil {
		return x.BelongsToAccount
	}
	return ""
}

func (x *Purchase) GetProductId() string {
	if x != nil {
		return x.ProductId
	}
	return ""
}

func (x *Purchase) GetExternalTransactionId() string {
	if x != nil {
		return x.ExternalTransactionId
	}
	return ""
}

func (x *Purchase) GetCurrency() string {
	if x != nil {
		return x.Currency
	}
	return ""
}

func (x *Purchase) GetAmountCents() int64 {
	if x != nil {
		return x.AmountCents
	}
	return 0
}

// Transaction is what one attempt to move money left behind.
type Transaction struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// created_at is when the attempt was recorded, which is also the order the
	// ledger walks in.
	CreatedAt *timestamppb.Timestamp `protobuf:"bytes,1,opt,name=created_at,json=createdAt,proto3" json:"created_at,omitempty"`
	// last_updated_at is when the row last changed, which for this table means
	// when its status last moved.
	LastUpdatedAt *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=last_updated_at,json=lastUpdatedAt,proto3" json:"last_updated_at,omitempty"`
	// archived_at is when the row was retired administratively -- for the row
	// written in error, and not for a refund.
	ArchivedAt *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=archived_at,json=archivedAt,proto3" json:"archived_at,omitempty"`
	// id is the row.
	Id string `protobuf:"bytes,4,opt,name=id,proto3" json:"id,omitempty"`
	// belongs_to_account is whose money moved.
	BelongsToAccount string `protobuf:"bytes,5,opt,name=belongs_to_account,json=belongsToAccount,proto3" json:"belongs_to_account,omitempty"`
	// subscription_id is the agreement this attempt renewed, and purchase_id the
	// sale it paid for. At most one of them is set: an attempt settles one or the
	// other, and a row claiming both is one a reconciliation would count twice.
	// Both empty is legal and means neither is still here, which is what a refund
	// of something since removed looks like.
	SubscriptionId string `protobuf:"bytes,6,opt,name=subscription_id,json=subscriptionID,proto3" json:"subscription_id,omitempty"`
	PurchaseId     string `protobuf:"bytes,7,opt,name=purchase_id,json=purchaseID,proto3" json:"purchase_id,omitempty"`
	// external_transaction_id is the provider's identifier for the attempt, empty
	// for a row with no provider behind it. It is the column that makes the
	// ledger safe to write from a webhook: providers redeliver, and the unique
	// index over it turns a second delivery into a refusal instead of a second
	// row.
	ExternalTransactionId string `protobuf:"bytes,8,opt,name=external_transaction_id,json=externalTransactionID,proto3" json:"external_transaction_id,omitempty"`
	// status is what became of the attempt.
	Status TransactionStatus `protobuf:"varint,9,opt,name=status,proto3,enum=primandproper.platform.billing.v1.TransactionStatus" json:"status,omitempty"`
	// currency is the ISO 4217 code amount_cents is in, upper case.
	Currency string `protobuf:"bytes,10,opt,name=currency,proto3" json:"currency,omitempty"`
	// amount_cents is the amount this attempt moved, in the currency's minor unit
	// -- which for a partial refund is not the price of anything.
	AmountCents   int64 `protobuf:"varint,11,opt,name=amount_cents,json=amountCents,proto3" json:"amount_cents,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Transaction) Reset() {
	*x = Transaction{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Transaction) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Transaction) ProtoMessage() {}

func (x *Transaction) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Transaction.ProtoReflect.Descriptor instead.
func (*Transaction) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{3}
}

func (x *Transaction) GetCreatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.CreatedAt
	}
	return nil
}

func (x *Transaction) GetLastUpdatedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.LastUpdatedAt
	}
	return nil
}

func (x *Transaction) GetArchivedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ArchivedAt
	}
	return nil
}

func (x *Transaction) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *Transaction) GetBelongsToAccount() string {
	if x != nil {
		return x.BelongsToAccount
	}
	return ""
}

func (x *Transaction) GetSubscriptionId() string {
	if x != nil {
		return x.SubscriptionId
	}
	return ""
}

func (x *Transaction) GetPurchaseId() string {
	if x != nil {
		return x.PurchaseId
	}
	return ""
}

func (x *Transaction) GetExternalTransactionId() string {
	if x != nil {
		return x.ExternalTransactionId
	}
	return ""
}

func (x *Transaction) GetStatus() TransactionStatus {
	if x != nil {
		return x.Status
	}
	return TransactionStatus_TRANSACTION_STATUS_UNSPECIFIED
}

func (x *Transaction) GetCurrency() string {
	if x != nil {
		return x.Currency
	}
	return ""
}

func (x *Transaction) GetAmountCents() int64 {
	if x != nil {
		return x.AmountCents
	}
	return 0
}

// ProductCreationInput is what a caller supplies to stock the catalog.
// Everything else about the row -- the id, the timestamps and the scope -- is
// decided by the service.
type ProductCreationInput struct {
	state                 protoimpl.MessageState `protogen:"open.v1"`
	Name                  string                 `protobuf:"bytes,1,opt,name=name,proto3" json:"name,omitempty"`
	Description           string                 `protobuf:"bytes,2,opt,name=description,proto3" json:"description,omitempty"`
	Kind                  ProductKind            `protobuf:"varint,3,opt,name=kind,proto3,enum=primandproper.platform.billing.v1.ProductKind" json:"kind,omitempty"`
	Currency              string                 `protobuf:"bytes,4,opt,name=currency,proto3" json:"currency,omitempty"`
	ExternalProductId     string                 `protobuf:"bytes,5,opt,name=external_product_id,json=externalProductID,proto3" json:"external_product_id,omitempty"`
	AmountCents           int64                  `protobuf:"varint,6,opt,name=amount_cents,json=amountCents,proto3" json:"amount_cents,omitempty"`
	BillingIntervalMonths int64                  `protobuf:"varint,7,opt,name=billing_interval_months,json=billingIntervalMonths,proto3" json:"billing_interval_months,omitempty"`
	unknownFields         protoimpl.UnknownFields
	sizeCache             protoimpl.SizeCache
}

func (x *ProductCreationInput) Reset() {
	*x = ProductCreationInput{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ProductCreationInput) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ProductCreationInput) ProtoMessage() {}

func (x *ProductCreationInput) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ProductCreationInput.ProtoReflect.Descriptor instead.
func (*ProductCreationInput) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{4}
}

func (x *ProductCreationInput) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *ProductCreationInput) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

func (x *ProductCreationInput) GetKind() ProductKind {
	if x != nil {
		return x.Kind
	}
	return ProductKind_PRODUCT_KIND_UNSPECIFIED
}

func (x *ProductCreationInput) GetCurrency() string {
	if x != nil {
		return x.Currency
	}
	return ""
}

func (x *ProductCreationInput) GetExternalProductId() string {
	if x != nil {
		return x.ExternalProductId
	}
	return ""
}

func (x *ProductCreationInput) GetAmountCents() int64 {
	if x != nil {
		return x.AmountCents
	}
	return 0
}

func (x *ProductCreationInput) GetBillingIntervalMonths() int64 {
	if x != nil {
		return x.BillingIntervalMonths
	}
	return 0
}

// ProductUpdateInput restates every revisable fact about a product.
//
// It is a full statement rather than a patch, and it is a separate message from
// [ProductCreationInput] though it carries the same seven fields, because the
// store's update assigns all seven together: a revision able to write half of a
// product would leave a catalog entry whose price and recurrence disagree, and
// nothing would report it. A caller revising one field sends the other six back
// as they read them.
type ProductUpdateInput struct {
	state                 protoimpl.MessageState `protogen:"open.v1"`
	Name                  string                 `protobuf:"bytes,1,opt,name=name,proto3" json:"name,omitempty"`
	Description           string                 `protobuf:"bytes,2,opt,name=description,proto3" json:"description,omitempty"`
	Kind                  ProductKind            `protobuf:"varint,3,opt,name=kind,proto3,enum=primandproper.platform.billing.v1.ProductKind" json:"kind,omitempty"`
	Currency              string                 `protobuf:"bytes,4,opt,name=currency,proto3" json:"currency,omitempty"`
	ExternalProductId     string                 `protobuf:"bytes,5,opt,name=external_product_id,json=externalProductID,proto3" json:"external_product_id,omitempty"`
	AmountCents           int64                  `protobuf:"varint,6,opt,name=amount_cents,json=amountCents,proto3" json:"amount_cents,omitempty"`
	BillingIntervalMonths int64                  `protobuf:"varint,7,opt,name=billing_interval_months,json=billingIntervalMonths,proto3" json:"billing_interval_months,omitempty"`
	unknownFields         protoimpl.UnknownFields
	sizeCache             protoimpl.SizeCache
}

func (x *ProductUpdateInput) Reset() {
	*x = ProductUpdateInput{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ProductUpdateInput) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ProductUpdateInput) ProtoMessage() {}

func (x *ProductUpdateInput) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ProductUpdateInput.ProtoReflect.Descriptor instead.
func (*ProductUpdateInput) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{5}
}

func (x *ProductUpdateInput) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *ProductUpdateInput) GetDescription() string {
	if x != nil {
		return x.Description
	}
	return ""
}

func (x *ProductUpdateInput) GetKind() ProductKind {
	if x != nil {
		return x.Kind
	}
	return ProductKind_PRODUCT_KIND_UNSPECIFIED
}

func (x *ProductUpdateInput) GetCurrency() string {
	if x != nil {
		return x.Currency
	}
	return ""
}

func (x *ProductUpdateInput) GetExternalProductId() string {
	if x != nil {
		return x.ExternalProductId
	}
	return ""
}

func (x *ProductUpdateInput) GetAmountCents() int64 {
	if x != nil {
		return x.AmountCents
	}
	return 0
}

func (x *ProductUpdateInput) GetBillingIntervalMonths() int64 {
	if x != nil {
		return x.BillingIntervalMonths
	}
	return 0
}

type CreateProductRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Input         *ProductCreationInput  `protobuf:"bytes,1,opt,name=input,proto3" json:"input,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CreateProductRequest) Reset() {
	*x = CreateProductRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[6]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CreateProductRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CreateProductRequest) ProtoMessage() {}

func (x *CreateProductRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[6]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CreateProductRequest.ProtoReflect.Descriptor instead.
func (*CreateProductRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{6}
}

func (x *CreateProductRequest) GetInput() *ProductCreationInput {
	if x != nil {
		return x.Input
	}
	return nil
}

type CreateProductResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Product               `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CreateProductResponse) Reset() {
	*x = CreateProductResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[7]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CreateProductResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CreateProductResponse) ProtoMessage() {}

func (x *CreateProductResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[7]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CreateProductResponse.ProtoReflect.Descriptor instead.
func (*CreateProductResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{7}
}

func (x *CreateProductResponse) GetResult() *Product {
	if x != nil {
		return x.Result
	}
	return nil
}

type GetProductRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ProductId     string                 `protobuf:"bytes,1,opt,name=product_id,json=productID,proto3" json:"product_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetProductRequest) Reset() {
	*x = GetProductRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[8]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetProductRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetProductRequest) ProtoMessage() {}

func (x *GetProductRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[8]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetProductRequest.ProtoReflect.Descriptor instead.
func (*GetProductRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{8}
}

func (x *GetProductRequest) GetProductId() string {
	if x != nil {
		return x.ProductId
	}
	return ""
}

type GetProductResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Product               `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetProductResponse) Reset() {
	*x = GetProductResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[9]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetProductResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetProductResponse) ProtoMessage() {}

func (x *GetProductResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[9]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetProductResponse.ProtoReflect.Descriptor instead.
func (*GetProductResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{9}
}

func (x *GetProductResponse) GetResult() *Product {
	if x != nil {
		return x.Result
	}
	return nil
}

type ListProductsRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,1,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListProductsRequest) Reset() {
	*x = ListProductsRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[10]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListProductsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListProductsRequest) ProtoMessage() {}

func (x *ListProductsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[10]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListProductsRequest.ProtoReflect.Descriptor instead.
func (*ListProductsRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{10}
}

func (x *ListProductsRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListProductsResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Product              `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListProductsResponse) Reset() {
	*x = ListProductsResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[11]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListProductsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListProductsResponse) ProtoMessage() {}

func (x *ListProductsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[11]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListProductsResponse.ProtoReflect.Descriptor instead.
func (*ListProductsResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{11}
}

func (x *ListProductsResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListProductsResponse) GetResults() []*Product {
	if x != nil {
		return x.Results
	}
	return nil
}

type UpdateProductRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ProductId     string                 `protobuf:"bytes,1,opt,name=product_id,json=productID,proto3" json:"product_id,omitempty"`
	Input         *ProductUpdateInput    `protobuf:"bytes,2,opt,name=input,proto3" json:"input,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateProductRequest) Reset() {
	*x = UpdateProductRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[12]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateProductRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateProductRequest) ProtoMessage() {}

func (x *UpdateProductRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[12]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateProductRequest.ProtoReflect.Descriptor instead.
func (*UpdateProductRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{12}
}

func (x *UpdateProductRequest) GetProductId() string {
	if x != nil {
		return x.ProductId
	}
	return ""
}

func (x *UpdateProductRequest) GetInput() *ProductUpdateInput {
	if x != nil {
		return x.Input
	}
	return nil
}

type UpdateProductResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Product               `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateProductResponse) Reset() {
	*x = UpdateProductResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[13]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateProductResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateProductResponse) ProtoMessage() {}

func (x *UpdateProductResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[13]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateProductResponse.ProtoReflect.Descriptor instead.
func (*UpdateProductResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{13}
}

func (x *UpdateProductResponse) GetResult() *Product {
	if x != nil {
		return x.Result
	}
	return nil
}

type ArchiveProductRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ProductId     string                 `protobuf:"bytes,1,opt,name=product_id,json=productID,proto3" json:"product_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchiveProductRequest) Reset() {
	*x = ArchiveProductRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[14]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveProductRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveProductRequest) ProtoMessage() {}

func (x *ArchiveProductRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[14]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveProductRequest.ProtoReflect.Descriptor instead.
func (*ArchiveProductRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{14}
}

func (x *ArchiveProductRequest) GetProductId() string {
	if x != nil {
		return x.ProductId
	}
	return ""
}

type ArchiveProductResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchiveProductResponse) Reset() {
	*x = ArchiveProductResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[15]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveProductResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveProductResponse) ProtoMessage() {}

func (x *ArchiveProductResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[15]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveProductResponse.ProtoReflect.Descriptor instead.
func (*ArchiveProductResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{15}
}

type GetSubscriptionRequest struct {
	state          protoimpl.MessageState `protogen:"open.v1"`
	SubscriptionId string                 `protobuf:"bytes,1,opt,name=subscription_id,json=subscriptionID,proto3" json:"subscription_id,omitempty"`
	unknownFields  protoimpl.UnknownFields
	sizeCache      protoimpl.SizeCache
}

func (x *GetSubscriptionRequest) Reset() {
	*x = GetSubscriptionRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[16]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetSubscriptionRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetSubscriptionRequest) ProtoMessage() {}

func (x *GetSubscriptionRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[16]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetSubscriptionRequest.ProtoReflect.Descriptor instead.
func (*GetSubscriptionRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{16}
}

func (x *GetSubscriptionRequest) GetSubscriptionId() string {
	if x != nil {
		return x.SubscriptionId
	}
	return ""
}

type GetSubscriptionResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Subscription          `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetSubscriptionResponse) Reset() {
	*x = GetSubscriptionResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[17]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetSubscriptionResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetSubscriptionResponse) ProtoMessage() {}

func (x *GetSubscriptionResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[17]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetSubscriptionResponse.ProtoReflect.Descriptor instead.
func (*GetSubscriptionResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{17}
}

func (x *GetSubscriptionResponse) GetResult() *Subscription {
	if x != nil {
		return x.Result
	}
	return nil
}

type ListSubscriptionsRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,1,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSubscriptionsRequest) Reset() {
	*x = ListSubscriptionsRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[18]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSubscriptionsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSubscriptionsRequest) ProtoMessage() {}

func (x *ListSubscriptionsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[18]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSubscriptionsRequest.ProtoReflect.Descriptor instead.
func (*ListSubscriptionsRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{18}
}

func (x *ListSubscriptionsRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListSubscriptionsResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Subscription         `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSubscriptionsResponse) Reset() {
	*x = ListSubscriptionsResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[19]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSubscriptionsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSubscriptionsResponse) ProtoMessage() {}

func (x *ListSubscriptionsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[19]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSubscriptionsResponse.ProtoReflect.Descriptor instead.
func (*ListSubscriptionsResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{19}
}

func (x *ListSubscriptionsResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListSubscriptionsResponse) GetResults() []*Subscription {
	if x != nil {
		return x.Results
	}
	return nil
}

type ListSubscriptionsForAccountRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	AccountId     string                   `protobuf:"bytes,1,opt,name=account_id,json=accountID,proto3" json:"account_id,omitempty"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,2,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSubscriptionsForAccountRequest) Reset() {
	*x = ListSubscriptionsForAccountRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[20]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSubscriptionsForAccountRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSubscriptionsForAccountRequest) ProtoMessage() {}

func (x *ListSubscriptionsForAccountRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[20]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSubscriptionsForAccountRequest.ProtoReflect.Descriptor instead.
func (*ListSubscriptionsForAccountRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{20}
}

func (x *ListSubscriptionsForAccountRequest) GetAccountId() string {
	if x != nil {
		return x.AccountId
	}
	return ""
}

func (x *ListSubscriptionsForAccountRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListSubscriptionsForAccountResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Subscription         `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSubscriptionsForAccountResponse) Reset() {
	*x = ListSubscriptionsForAccountResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[21]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSubscriptionsForAccountResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSubscriptionsForAccountResponse) ProtoMessage() {}

func (x *ListSubscriptionsForAccountResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[21]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSubscriptionsForAccountResponse.ProtoReflect.Descriptor instead.
func (*ListSubscriptionsForAccountResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{21}
}

func (x *ListSubscriptionsForAccountResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListSubscriptionsForAccountResponse) GetResults() []*Subscription {
	if x != nil {
		return x.Results
	}
	return nil
}

type ListCurrentSubscriptionsRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	AccountId     string                   `protobuf:"bytes,1,opt,name=account_id,json=accountID,proto3" json:"account_id,omitempty"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,2,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListCurrentSubscriptionsRequest) Reset() {
	*x = ListCurrentSubscriptionsRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[22]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListCurrentSubscriptionsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListCurrentSubscriptionsRequest) ProtoMessage() {}

func (x *ListCurrentSubscriptionsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[22]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListCurrentSubscriptionsRequest.ProtoReflect.Descriptor instead.
func (*ListCurrentSubscriptionsRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{22}
}

func (x *ListCurrentSubscriptionsRequest) GetAccountId() string {
	if x != nil {
		return x.AccountId
	}
	return ""
}

func (x *ListCurrentSubscriptionsRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListCurrentSubscriptionsResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Subscription         `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListCurrentSubscriptionsResponse) Reset() {
	*x = ListCurrentSubscriptionsResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[23]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListCurrentSubscriptionsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListCurrentSubscriptionsResponse) ProtoMessage() {}

func (x *ListCurrentSubscriptionsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[23]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListCurrentSubscriptionsResponse.ProtoReflect.Descriptor instead.
func (*ListCurrentSubscriptionsResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{23}
}

func (x *ListCurrentSubscriptionsResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListCurrentSubscriptionsResponse) GetResults() []*Subscription {
	if x != nil {
		return x.Results
	}
	return nil
}

type ArchiveSubscriptionRequest struct {
	state          protoimpl.MessageState `protogen:"open.v1"`
	SubscriptionId string                 `protobuf:"bytes,1,opt,name=subscription_id,json=subscriptionID,proto3" json:"subscription_id,omitempty"`
	unknownFields  protoimpl.UnknownFields
	sizeCache      protoimpl.SizeCache
}

func (x *ArchiveSubscriptionRequest) Reset() {
	*x = ArchiveSubscriptionRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[24]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveSubscriptionRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveSubscriptionRequest) ProtoMessage() {}

func (x *ArchiveSubscriptionRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[24]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveSubscriptionRequest.ProtoReflect.Descriptor instead.
func (*ArchiveSubscriptionRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{24}
}

func (x *ArchiveSubscriptionRequest) GetSubscriptionId() string {
	if x != nil {
		return x.SubscriptionId
	}
	return ""
}

type ArchiveSubscriptionResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchiveSubscriptionResponse) Reset() {
	*x = ArchiveSubscriptionResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[25]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveSubscriptionResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveSubscriptionResponse) ProtoMessage() {}

func (x *ArchiveSubscriptionResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[25]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveSubscriptionResponse.ProtoReflect.Descriptor instead.
func (*ArchiveSubscriptionResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{25}
}

type GetPurchaseRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	PurchaseId    string                 `protobuf:"bytes,1,opt,name=purchase_id,json=purchaseID,proto3" json:"purchase_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetPurchaseRequest) Reset() {
	*x = GetPurchaseRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[26]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetPurchaseRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetPurchaseRequest) ProtoMessage() {}

func (x *GetPurchaseRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[26]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetPurchaseRequest.ProtoReflect.Descriptor instead.
func (*GetPurchaseRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{26}
}

func (x *GetPurchaseRequest) GetPurchaseId() string {
	if x != nil {
		return x.PurchaseId
	}
	return ""
}

type GetPurchaseResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Purchase              `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetPurchaseResponse) Reset() {
	*x = GetPurchaseResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[27]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetPurchaseResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetPurchaseResponse) ProtoMessage() {}

func (x *GetPurchaseResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[27]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetPurchaseResponse.ProtoReflect.Descriptor instead.
func (*GetPurchaseResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{27}
}

func (x *GetPurchaseResponse) GetResult() *Purchase {
	if x != nil {
		return x.Result
	}
	return nil
}

type ListPurchasesRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,1,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListPurchasesRequest) Reset() {
	*x = ListPurchasesRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[28]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListPurchasesRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListPurchasesRequest) ProtoMessage() {}

func (x *ListPurchasesRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[28]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListPurchasesRequest.ProtoReflect.Descriptor instead.
func (*ListPurchasesRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{28}
}

func (x *ListPurchasesRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListPurchasesResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Purchase             `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListPurchasesResponse) Reset() {
	*x = ListPurchasesResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[29]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListPurchasesResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListPurchasesResponse) ProtoMessage() {}

func (x *ListPurchasesResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[29]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListPurchasesResponse.ProtoReflect.Descriptor instead.
func (*ListPurchasesResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{29}
}

func (x *ListPurchasesResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListPurchasesResponse) GetResults() []*Purchase {
	if x != nil {
		return x.Results
	}
	return nil
}

type ListPurchasesForAccountRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	AccountId     string                   `protobuf:"bytes,1,opt,name=account_id,json=accountID,proto3" json:"account_id,omitempty"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,2,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListPurchasesForAccountRequest) Reset() {
	*x = ListPurchasesForAccountRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[30]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListPurchasesForAccountRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListPurchasesForAccountRequest) ProtoMessage() {}

func (x *ListPurchasesForAccountRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[30]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListPurchasesForAccountRequest.ProtoReflect.Descriptor instead.
func (*ListPurchasesForAccountRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{30}
}

func (x *ListPurchasesForAccountRequest) GetAccountId() string {
	if x != nil {
		return x.AccountId
	}
	return ""
}

func (x *ListPurchasesForAccountRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListPurchasesForAccountResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Purchase             `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListPurchasesForAccountResponse) Reset() {
	*x = ListPurchasesForAccountResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[31]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListPurchasesForAccountResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListPurchasesForAccountResponse) ProtoMessage() {}

func (x *ListPurchasesForAccountResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[31]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListPurchasesForAccountResponse.ProtoReflect.Descriptor instead.
func (*ListPurchasesForAccountResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{31}
}

func (x *ListPurchasesForAccountResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListPurchasesForAccountResponse) GetResults() []*Purchase {
	if x != nil {
		return x.Results
	}
	return nil
}

type ArchivePurchaseRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	PurchaseId    string                 `protobuf:"bytes,1,opt,name=purchase_id,json=purchaseID,proto3" json:"purchase_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchivePurchaseRequest) Reset() {
	*x = ArchivePurchaseRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[32]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchivePurchaseRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchivePurchaseRequest) ProtoMessage() {}

func (x *ArchivePurchaseRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[32]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchivePurchaseRequest.ProtoReflect.Descriptor instead.
func (*ArchivePurchaseRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{32}
}

func (x *ArchivePurchaseRequest) GetPurchaseId() string {
	if x != nil {
		return x.PurchaseId
	}
	return ""
}

type ArchivePurchaseResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchivePurchaseResponse) Reset() {
	*x = ArchivePurchaseResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[33]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchivePurchaseResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchivePurchaseResponse) ProtoMessage() {}

func (x *ArchivePurchaseResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[33]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchivePurchaseResponse.ProtoReflect.Descriptor instead.
func (*ArchivePurchaseResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{33}
}

type GetTransactionRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	TransactionId string                 `protobuf:"bytes,1,opt,name=transaction_id,json=transactionID,proto3" json:"transaction_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetTransactionRequest) Reset() {
	*x = GetTransactionRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[34]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetTransactionRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetTransactionRequest) ProtoMessage() {}

func (x *GetTransactionRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[34]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetTransactionRequest.ProtoReflect.Descriptor instead.
func (*GetTransactionRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{34}
}

func (x *GetTransactionRequest) GetTransactionId() string {
	if x != nil {
		return x.TransactionId
	}
	return ""
}

type GetTransactionResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *Transaction           `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetTransactionResponse) Reset() {
	*x = GetTransactionResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[35]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetTransactionResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetTransactionResponse) ProtoMessage() {}

func (x *GetTransactionResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[35]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetTransactionResponse.ProtoReflect.Descriptor instead.
func (*GetTransactionResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{35}
}

func (x *GetTransactionResponse) GetResult() *Transaction {
	if x != nil {
		return x.Result
	}
	return nil
}

type ListTransactionsRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,1,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListTransactionsRequest) Reset() {
	*x = ListTransactionsRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[36]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListTransactionsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListTransactionsRequest) ProtoMessage() {}

func (x *ListTransactionsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[36]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListTransactionsRequest.ProtoReflect.Descriptor instead.
func (*ListTransactionsRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{36}
}

func (x *ListTransactionsRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListTransactionsResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Transaction          `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListTransactionsResponse) Reset() {
	*x = ListTransactionsResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[37]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListTransactionsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListTransactionsResponse) ProtoMessage() {}

func (x *ListTransactionsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[37]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListTransactionsResponse.ProtoReflect.Descriptor instead.
func (*ListTransactionsResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{37}
}

func (x *ListTransactionsResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListTransactionsResponse) GetResults() []*Transaction {
	if x != nil {
		return x.Results
	}
	return nil
}

type ListTransactionsForAccountRequest struct {
	state         protoimpl.MessageState   `protogen:"open.v1"`
	AccountId     string                   `protobuf:"bytes,1,opt,name=account_id,json=accountID,proto3" json:"account_id,omitempty"`
	Filter        *filteringpb.QueryFilter `protobuf:"bytes,2,opt,name=filter,proto3" json:"filter,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListTransactionsForAccountRequest) Reset() {
	*x = ListTransactionsForAccountRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[38]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListTransactionsForAccountRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListTransactionsForAccountRequest) ProtoMessage() {}

func (x *ListTransactionsForAccountRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[38]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListTransactionsForAccountRequest.ProtoReflect.Descriptor instead.
func (*ListTransactionsForAccountRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{38}
}

func (x *ListTransactionsForAccountRequest) GetAccountId() string {
	if x != nil {
		return x.AccountId
	}
	return ""
}

func (x *ListTransactionsForAccountRequest) GetFilter() *filteringpb.QueryFilter {
	if x != nil {
		return x.Filter
	}
	return nil
}

type ListTransactionsForAccountResponse struct {
	state         protoimpl.MessageState  `protogen:"open.v1"`
	Pagination    *filteringpb.Pagination `protobuf:"bytes,1,opt,name=pagination,proto3" json:"pagination,omitempty"`
	Results       []*Transaction          `protobuf:"bytes,2,rep,name=results,proto3" json:"results,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListTransactionsForAccountResponse) Reset() {
	*x = ListTransactionsForAccountResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[39]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListTransactionsForAccountResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListTransactionsForAccountResponse) ProtoMessage() {}

func (x *ListTransactionsForAccountResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[39]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListTransactionsForAccountResponse.ProtoReflect.Descriptor instead.
func (*ListTransactionsForAccountResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{39}
}

func (x *ListTransactionsForAccountResponse) GetPagination() *filteringpb.Pagination {
	if x != nil {
		return x.Pagination
	}
	return nil
}

func (x *ListTransactionsForAccountResponse) GetResults() []*Transaction {
	if x != nil {
		return x.Results
	}
	return nil
}

type ArchiveTransactionRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	TransactionId string                 `protobuf:"bytes,1,opt,name=transaction_id,json=transactionID,proto3" json:"transaction_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchiveTransactionRequest) Reset() {
	*x = ArchiveTransactionRequest{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[40]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveTransactionRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveTransactionRequest) ProtoMessage() {}

func (x *ArchiveTransactionRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[40]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveTransactionRequest.ProtoReflect.Descriptor instead.
func (*ArchiveTransactionRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{40}
}

func (x *ArchiveTransactionRequest) GetTransactionId() string {
	if x != nil {
		return x.TransactionId
	}
	return ""
}

type ArchiveTransactionResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ArchiveTransactionResponse) Reset() {
	*x = ArchiveTransactionResponse{}
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[41]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ArchiveTransactionResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ArchiveTransactionResponse) ProtoMessage() {}

func (x *ArchiveTransactionResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_billing_v1_billing_proto_msgTypes[41]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ArchiveTransactionResponse.ProtoReflect.Descriptor instead.
func (*ArchiveTransactionResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP(), []int{41}
}

var File_primandproper_platform_billing_v1_billing_proto protoreflect.FileDescriptor

const file_primandproper_platform_billing_v1_billing_proto_rawDesc = "" +
	"\n" +
	"/primandproper/platform/billing/v1/billing.proto\x12!primandproper.platform.billing.v1\x1a\x1fgoogle/protobuf/timestamp.proto\x1a3primandproper/platform/filtering/v1/filtering.proto\"\xfd\x03\n" +
	"\aProduct\x129\n" +
	"\n" +
	"created_at\x18\x01 \x01(\v2\x1a.google.protobuf.TimestampR\tcreatedAt\x12B\n" +
	"\x0flast_updated_at\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\rlastUpdatedAt\x12;\n" +
	"\varchived_at\x18\x03 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"archivedAt\x12\x0e\n" +
	"\x02id\x18\x04 \x01(\tR\x02id\x12\x12\n" +
	"\x04name\x18\x05 \x01(\tR\x04name\x12 \n" +
	"\vdescription\x18\x06 \x01(\tR\vdescription\x12B\n" +
	"\x04kind\x18\a \x01(\x0e2..primandproper.platform.billing.v1.ProductKindR\x04kind\x12\x1a\n" +
	"\bcurrency\x18\b \x01(\tR\bcurrency\x12.\n" +
	"\x13external_product_id\x18\t \x01(\tR\x11externalProductID\x12!\n" +
	"\famount_cents\x18\n" +
	" \x01(\x03R\vamountCents\x126\n" +
	"\x17billing_interval_months\x18\v \x01(\x03R\x15billingIntervalMonthsR\x05scope\"\x98\x04\n" +
	"\fSubscription\x129\n" +
	"\n" +
	"created_at\x18\x01 \x01(\v2\x1a.google.protobuf.TimestampR\tcreatedAt\x12L\n" +
	"\x14current_period_start\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\x12currentPeriodStart\x12H\n" +
	"\x12current_period_end\x18\x03 \x01(\v2\x1a.google.protobuf.TimestampR\x10currentPeriodEnd\x12B\n" +
	"\x0flast_updated_at\x18\x04 \x01(\v2\x1a.google.protobuf.TimestampR\rlastUpdatedAt\x12;\n" +
	"\varchived_at\x18\x05 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"archivedAt\x12\x0e\n" +
	"\x02id\x18\x06 \x01(\tR\x02id\x12,\n" +
	"\x12belongs_to_account\x18\a \x01(\tR\x10belongsToAccount\x12\x1d\n" +
	"\n" +
	"product_id\x18\b \x01(\tR\tproductID\x128\n" +
	"\x18external_subscription_id\x18\t \x01(\tR\x16externalSubscriptionID\x12\x16\n" +
	"\x06status\x18\n" +
	" \x01(\tR\x06statusR\x05scope\"\xe0\x03\n" +
	"\bPurchase\x129\n" +
	"\n" +
	"created_at\x18\x01 \x01(\v2\x1a.google.protobuf.TimestampR\tcreatedAt\x12=\n" +
	"\fcompleted_at\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\vcompletedAt\x12B\n" +
	"\x0flast_updated_at\x18\x03 \x01(\v2\x1a.google.protobuf.TimestampR\rlastUpdatedAt\x12;\n" +
	"\varchived_at\x18\x04 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"archivedAt\x12\x0e\n" +
	"\x02id\x18\x05 \x01(\tR\x02id\x12,\n" +
	"\x12belongs_to_account\x18\x06 \x01(\tR\x10belongsToAccount\x12\x1d\n" +
	"\n" +
	"product_id\x18\a \x01(\tR\tproductID\x126\n" +
	"\x17external_transaction_id\x18\b \x01(\tR\x15externalTransactionID\x12\x1a\n" +
	"\bcurrency\x18\t \x01(\tR\bcurrency\x12!\n" +
	"\famount_cents\x18\n" +
	" \x01(\x03R\vamountCentsR\x05scope\"\x9d\x04\n" +
	"\vTransaction\x129\n" +
	"\n" +
	"created_at\x18\x01 \x01(\v2\x1a.google.protobuf.TimestampR\tcreatedAt\x12B\n" +
	"\x0flast_updated_at\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\rlastUpdatedAt\x12;\n" +
	"\varchived_at\x18\x03 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"archivedAt\x12\x0e\n" +
	"\x02id\x18\x04 \x01(\tR\x02id\x12,\n" +
	"\x12belongs_to_account\x18\x05 \x01(\tR\x10belongsToAccount\x12'\n" +
	"\x0fsubscription_id\x18\x06 \x01(\tR\x0esubscriptionID\x12\x1f\n" +
	"\vpurchase_id\x18\a \x01(\tR\n" +
	"purchaseID\x126\n" +
	"\x17external_transaction_id\x18\b \x01(\tR\x15externalTransactionID\x12L\n" +
	"\x06status\x18\t \x01(\x0e24.primandproper.platform.billing.v1.TransactionStatusR\x06status\x12\x1a\n" +
	"\bcurrency\x18\n" +
	" \x01(\tR\bcurrency\x12!\n" +
	"\famount_cents\x18\v \x01(\x03R\vamountCentsR\x05scope\"\xbe\x02\n" +
	"\x14ProductCreationInput\x12\x12\n" +
	"\x04name\x18\x01 \x01(\tR\x04name\x12 \n" +
	"\vdescription\x18\x02 \x01(\tR\vdescription\x12B\n" +
	"\x04kind\x18\x03 \x01(\x0e2..primandproper.platform.billing.v1.ProductKindR\x04kind\x12\x1a\n" +
	"\bcurrency\x18\x04 \x01(\tR\bcurrency\x12.\n" +
	"\x13external_product_id\x18\x05 \x01(\tR\x11externalProductID\x12!\n" +
	"\famount_cents\x18\x06 \x01(\x03R\vamountCents\x126\n" +
	"\x17billing_interval_months\x18\a \x01(\x03R\x15billingIntervalMonthsR\x05scope\"\xbc\x02\n" +
	"\x12ProductUpdateInput\x12\x12\n" +
	"\x04name\x18\x01 \x01(\tR\x04name\x12 \n" +
	"\vdescription\x18\x02 \x01(\tR\vdescription\x12B\n" +
	"\x04kind\x18\x03 \x01(\x0e2..primandproper.platform.billing.v1.ProductKindR\x04kind\x12\x1a\n" +
	"\bcurrency\x18\x04 \x01(\tR\bcurrency\x12.\n" +
	"\x13external_product_id\x18\x05 \x01(\tR\x11externalProductID\x12!\n" +
	"\famount_cents\x18\x06 \x01(\x03R\vamountCents\x126\n" +
	"\x17billing_interval_months\x18\a \x01(\x03R\x15billingIntervalMonthsR\x05scope\"l\n" +
	"\x14CreateProductRequest\x12M\n" +
	"\x05input\x18\x01 \x01(\v27.primandproper.platform.billing.v1.ProductCreationInputR\x05inputR\x05scope\"b\n" +
	"\x15CreateProductResponse\x12B\n" +
	"\x06result\x18\x01 \x01(\v2*.primandproper.platform.billing.v1.ProductR\x06resultR\x05scope\"9\n" +
	"\x11GetProductRequest\x12\x1d\n" +
	"\n" +
	"product_id\x18\x01 \x01(\tR\tproductIDR\x05scope\"_\n" +
	"\x12GetProductResponse\x12B\n" +
	"\x06result\x18\x01 \x01(\v2*.primandproper.platform.billing.v1.ProductR\x06resultR\x05scope\"f\n" +
	"\x13ListProductsRequest\x12H\n" +
	"\x06filter\x18\x01 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xb4\x01\n" +
	"\x14ListProductsResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12D\n" +
	"\aresults\x18\x02 \x03(\v2*.primandproper.platform.billing.v1.ProductR\aresultsR\x05scope\"\x89\x01\n" +
	"\x14UpdateProductRequest\x12\x1d\n" +
	"\n" +
	"product_id\x18\x01 \x01(\tR\tproductID\x12K\n" +
	"\x05input\x18\x02 \x01(\v25.primandproper.platform.billing.v1.ProductUpdateInputR\x05inputR\x05scope\"b\n" +
	"\x15UpdateProductResponse\x12B\n" +
	"\x06result\x18\x01 \x01(\v2*.primandproper.platform.billing.v1.ProductR\x06resultR\x05scope\"=\n" +
	"\x15ArchiveProductRequest\x12\x1d\n" +
	"\n" +
	"product_id\x18\x01 \x01(\tR\tproductIDR\x05scope\"\x1f\n" +
	"\x16ArchiveProductResponseR\x05scope\"H\n" +
	"\x16GetSubscriptionRequest\x12'\n" +
	"\x0fsubscription_id\x18\x01 \x01(\tR\x0esubscriptionIDR\x05scope\"i\n" +
	"\x17GetSubscriptionResponse\x12G\n" +
	"\x06result\x18\x01 \x01(\v2/.primandproper.platform.billing.v1.SubscriptionR\x06resultR\x05scope\"k\n" +
	"\x18ListSubscriptionsRequest\x12H\n" +
	"\x06filter\x18\x01 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xbe\x01\n" +
	"\x19ListSubscriptionsResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12I\n" +
	"\aresults\x18\x02 \x03(\v2/.primandproper.platform.billing.v1.SubscriptionR\aresultsR\x05scope\"\x94\x01\n" +
	"\"ListSubscriptionsForAccountRequest\x12\x1d\n" +
	"\n" +
	"account_id\x18\x01 \x01(\tR\taccountID\x12H\n" +
	"\x06filter\x18\x02 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xc8\x01\n" +
	"#ListSubscriptionsForAccountResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12I\n" +
	"\aresults\x18\x02 \x03(\v2/.primandproper.platform.billing.v1.SubscriptionR\aresultsR\x05scope\"\x91\x01\n" +
	"\x1fListCurrentSubscriptionsRequest\x12\x1d\n" +
	"\n" +
	"account_id\x18\x01 \x01(\tR\taccountID\x12H\n" +
	"\x06filter\x18\x02 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xc5\x01\n" +
	" ListCurrentSubscriptionsResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12I\n" +
	"\aresults\x18\x02 \x03(\v2/.primandproper.platform.billing.v1.SubscriptionR\aresultsR\x05scope\"L\n" +
	"\x1aArchiveSubscriptionRequest\x12'\n" +
	"\x0fsubscription_id\x18\x01 \x01(\tR\x0esubscriptionIDR\x05scope\"$\n" +
	"\x1bArchiveSubscriptionResponseR\x05scope\"<\n" +
	"\x12GetPurchaseRequest\x12\x1f\n" +
	"\vpurchase_id\x18\x01 \x01(\tR\n" +
	"purchaseIDR\x05scope\"a\n" +
	"\x13GetPurchaseResponse\x12C\n" +
	"\x06result\x18\x01 \x01(\v2+.primandproper.platform.billing.v1.PurchaseR\x06resultR\x05scope\"g\n" +
	"\x14ListPurchasesRequest\x12H\n" +
	"\x06filter\x18\x01 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xb6\x01\n" +
	"\x15ListPurchasesResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12E\n" +
	"\aresults\x18\x02 \x03(\v2+.primandproper.platform.billing.v1.PurchaseR\aresultsR\x05scope\"\x90\x01\n" +
	"\x1eListPurchasesForAccountRequest\x12\x1d\n" +
	"\n" +
	"account_id\x18\x01 \x01(\tR\taccountID\x12H\n" +
	"\x06filter\x18\x02 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xc0\x01\n" +
	"\x1fListPurchasesForAccountResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12E\n" +
	"\aresults\x18\x02 \x03(\v2+.primandproper.platform.billing.v1.PurchaseR\aresultsR\x05scope\"@\n" +
	"\x16ArchivePurchaseRequest\x12\x1f\n" +
	"\vpurchase_id\x18\x01 \x01(\tR\n" +
	"purchaseIDR\x05scope\" \n" +
	"\x17ArchivePurchaseResponseR\x05scope\"E\n" +
	"\x15GetTransactionRequest\x12%\n" +
	"\x0etransaction_id\x18\x01 \x01(\tR\rtransactionIDR\x05scope\"g\n" +
	"\x16GetTransactionResponse\x12F\n" +
	"\x06result\x18\x01 \x01(\v2..primandproper.platform.billing.v1.TransactionR\x06resultR\x05scope\"j\n" +
	"\x17ListTransactionsRequest\x12H\n" +
	"\x06filter\x18\x01 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xbc\x01\n" +
	"\x18ListTransactionsResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12H\n" +
	"\aresults\x18\x02 \x03(\v2..primandproper.platform.billing.v1.TransactionR\aresultsR\x05scope\"\x93\x01\n" +
	"!ListTransactionsForAccountRequest\x12\x1d\n" +
	"\n" +
	"account_id\x18\x01 \x01(\tR\taccountID\x12H\n" +
	"\x06filter\x18\x02 \x01(\v20.primandproper.platform.filtering.v1.QueryFilterR\x06filterR\x05scope\"\xc6\x01\n" +
	"\"ListTransactionsForAccountResponse\x12O\n" +
	"\n" +
	"pagination\x18\x01 \x01(\v2/.primandproper.platform.filtering.v1.PaginationR\n" +
	"pagination\x12H\n" +
	"\aresults\x18\x02 \x03(\v2..primandproper.platform.billing.v1.TransactionR\aresultsR\x05scope\"I\n" +
	"\x19ArchiveTransactionRequest\x12%\n" +
	"\x0etransaction_id\x18\x01 \x01(\tR\rtransactionIDR\x05scope\"#\n" +
	"\x1aArchiveTransactionResponseR\x05scope*b\n" +
	"\vProductKind\x12\x1c\n" +
	"\x18PRODUCT_KIND_UNSPECIFIED\x10\x00\x12\x1a\n" +
	"\x16PRODUCT_KIND_RECURRING\x10\x01\x12\x19\n" +
	"\x15PRODUCT_KIND_ONE_TIME\x10\x02*\xb9\x01\n" +
	"\x11TransactionStatus\x12\"\n" +
	"\x1eTRANSACTION_STATUS_UNSPECIFIED\x10\x00\x12\x1e\n" +
	"\x1aTRANSACTION_STATUS_PENDING\x10\x01\x12 \n" +
	"\x1cTRANSACTION_STATUS_SUCCEEDED\x10\x02\x12\x1d\n" +
	"\x19TRANSACTION_STATUS_FAILED\x10\x03\x12\x1f\n" +
	"\x1bTRANSACTION_STATUS_REFUNDED\x10\x042\xad\x14\n" +
	"\x0eBillingService\x12\x82\x01\n" +
	"\rCreateProduct\x127.primandproper.platform.billing.v1.CreateProductRequest\x1a8.primandproper.platform.billing.v1.CreateProductResponse\x12y\n" +
	"\n" +
	"GetProduct\x124.primandproper.platform.billing.v1.GetProductRequest\x1a5.primandproper.platform.billing.v1.GetProductResponse\x12\x7f\n" +
	"\fListProducts\x126.primandproper.platform.billing.v1.ListProductsRequest\x1a7.primandproper.platform.billing.v1.ListProductsResponse\x12\x82\x01\n" +
	"\rUpdateProduct\x127.primandproper.platform.billing.v1.UpdateProductRequest\x1a8.primandproper.platform.billing.v1.UpdateProductResponse\x12\x85\x01\n" +
	"\x0eArchiveProduct\x128.primandproper.platform.billing.v1.ArchiveProductRequest\x1a9.primandproper.platform.billing.v1.ArchiveProductResponse\x12\x88\x01\n" +
	"\x0fGetSubscription\x129.primandproper.platform.billing.v1.GetSubscriptionRequest\x1a:.primandproper.platform.billing.v1.GetSubscriptionResponse\x12\x8e\x01\n" +
	"\x11ListSubscriptions\x12;.primandproper.platform.billing.v1.ListSubscriptionsRequest\x1a<.primandproper.platform.billing.v1.ListSubscriptionsResponse\x12\xac\x01\n" +
	"\x1bListSubscriptionsForAccount\x12E.primandproper.platform.billing.v1.ListSubscriptionsForAccountRequest\x1aF.primandproper.platform.billing.v1.ListSubscriptionsForAccountResponse\x12\xa3\x01\n" +
	"\x18ListCurrentSubscriptions\x12B.primandproper.platform.billing.v1.ListCurrentSubscriptionsRequest\x1aC.primandproper.platform.billing.v1.ListCurrentSubscriptionsResponse\x12\x94\x01\n" +
	"\x13ArchiveSubscription\x12=.primandproper.platform.billing.v1.ArchiveSubscriptionRequest\x1a>.primandproper.platform.billing.v1.ArchiveSubscriptionResponse\x12|\n" +
	"\vGetPurchase\x125.primandproper.platform.billing.v1.GetPurchaseRequest\x1a6.primandproper.platform.billing.v1.GetPurchaseResponse\x12\x82\x01\n" +
	"\rListPurchases\x127.primandproper.platform.billing.v1.ListPurchasesRequest\x1a8.primandproper.platform.billing.v1.ListPurchasesResponse\x12\xa0\x01\n" +
	"\x17ListPurchasesForAccount\x12A.primandproper.platform.billing.v1.ListPurchasesForAccountRequest\x1aB.primandproper.platform.billing.v1.ListPurchasesForAccountResponse\x12\x88\x01\n" +
	"\x0fArchivePurchase\x129.primandproper.platform.billing.v1.ArchivePurchaseRequest\x1a:.primandproper.platform.billing.v1.ArchivePurchaseResponse\x12\x85\x01\n" +
	"\x0eGetTransaction\x128.primandproper.platform.billing.v1.GetTransactionRequest\x1a9.primandproper.platform.billing.v1.GetTransactionResponse\x12\x8b\x01\n" +
	"\x10ListTransactions\x12:.primandproper.platform.billing.v1.ListTransactionsRequest\x1a;.primandproper.platform.billing.v1.ListTransactionsResponse\x12\xa9\x01\n" +
	"\x1aListTransactionsForAccount\x12D.primandproper.platform.billing.v1.ListTransactionsForAccountRequest\x1aE.primandproper.platform.billing.v1.ListTransactionsForAccountResponse\x12\x91\x01\n" +
	"\x12ArchiveTransaction\x12<.primandproper.platform.billing.v1.ArchiveTransactionRequest\x1a=.primandproper.platform.billing.v1.ArchiveTransactionResponseBFZDgithub.com/primandproper/platform-go/v14/billing/billingpb;billingpbb\x06proto3"

var (
	file_primandproper_platform_billing_v1_billing_proto_rawDescOnce sync.Once
	file_primandproper_platform_billing_v1_billing_proto_rawDescData []byte
)

func file_primandproper_platform_billing_v1_billing_proto_rawDescGZIP() []byte {
	file_primandproper_platform_billing_v1_billing_proto_rawDescOnce.Do(func() {
		file_primandproper_platform_billing_v1_billing_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_primandproper_platform_billing_v1_billing_proto_rawDesc), len(file_primandproper_platform_billing_v1_billing_proto_rawDesc)))
	})
	return file_primandproper_platform_billing_v1_billing_proto_rawDescData
}

var file_primandproper_platform_billing_v1_billing_proto_enumTypes = make([]protoimpl.EnumInfo, 2)
var file_primandproper_platform_billing_v1_billing_proto_msgTypes = make([]protoimpl.MessageInfo, 42)
var file_primandproper_platform_billing_v1_billing_proto_goTypes = []any{
	(ProductKind)(0),                            // 0: primandproper.platform.billing.v1.ProductKind
	(TransactionStatus)(0),                      // 1: primandproper.platform.billing.v1.TransactionStatus
	(*Product)(nil),                             // 2: primandproper.platform.billing.v1.Product
	(*Subscription)(nil),                        // 3: primandproper.platform.billing.v1.Subscription
	(*Purchase)(nil),                            // 4: primandproper.platform.billing.v1.Purchase
	(*Transaction)(nil),                         // 5: primandproper.platform.billing.v1.Transaction
	(*ProductCreationInput)(nil),                // 6: primandproper.platform.billing.v1.ProductCreationInput
	(*ProductUpdateInput)(nil),                  // 7: primandproper.platform.billing.v1.ProductUpdateInput
	(*CreateProductRequest)(nil),                // 8: primandproper.platform.billing.v1.CreateProductRequest
	(*CreateProductResponse)(nil),               // 9: primandproper.platform.billing.v1.CreateProductResponse
	(*GetProductRequest)(nil),                   // 10: primandproper.platform.billing.v1.GetProductRequest
	(*GetProductResponse)(nil),                  // 11: primandproper.platform.billing.v1.GetProductResponse
	(*ListProductsRequest)(nil),                 // 12: primandproper.platform.billing.v1.ListProductsRequest
	(*ListProductsResponse)(nil),                // 13: primandproper.platform.billing.v1.ListProductsResponse
	(*UpdateProductRequest)(nil),                // 14: primandproper.platform.billing.v1.UpdateProductRequest
	(*UpdateProductResponse)(nil),               // 15: primandproper.platform.billing.v1.UpdateProductResponse
	(*ArchiveProductRequest)(nil),               // 16: primandproper.platform.billing.v1.ArchiveProductRequest
	(*ArchiveProductResponse)(nil),              // 17: primandproper.platform.billing.v1.ArchiveProductResponse
	(*GetSubscriptionRequest)(nil),              // 18: primandproper.platform.billing.v1.GetSubscriptionRequest
	(*GetSubscriptionResponse)(nil),             // 19: primandproper.platform.billing.v1.GetSubscriptionResponse
	(*ListSubscriptionsRequest)(nil),            // 20: primandproper.platform.billing.v1.ListSubscriptionsRequest
	(*ListSubscriptionsResponse)(nil),           // 21: primandproper.platform.billing.v1.ListSubscriptionsResponse
	(*ListSubscriptionsForAccountRequest)(nil),  // 22: primandproper.platform.billing.v1.ListSubscriptionsForAccountRequest
	(*ListSubscriptionsForAccountResponse)(nil), // 23: primandproper.platform.billing.v1.ListSubscriptionsForAccountResponse
	(*ListCurrentSubscriptionsRequest)(nil),     // 24: primandproper.platform.billing.v1.ListCurrentSubscriptionsRequest
	(*ListCurrentSubscriptionsResponse)(nil),    // 25: primandproper.platform.billing.v1.ListCurrentSubscriptionsResponse
	(*ArchiveSubscriptionRequest)(nil),          // 26: primandproper.platform.billing.v1.ArchiveSubscriptionRequest
	(*ArchiveSubscriptionResponse)(nil),         // 27: primandproper.platform.billing.v1.ArchiveSubscriptionResponse
	(*GetPurchaseRequest)(nil),                  // 28: primandproper.platform.billing.v1.GetPurchaseRequest
	(*GetPurchaseResponse)(nil),                 // 29: primandproper.platform.billing.v1.GetPurchaseResponse
	(*ListPurchasesRequest)(nil),                // 30: primandproper.platform.billing.v1.ListPurchasesRequest
	(*ListPurchasesResponse)(nil),               // 31: primandproper.platform.billing.v1.ListPurchasesResponse
	(*ListPurchasesForAccountRequest)(nil),      // 32: primandproper.platform.billing.v1.ListPurchasesForAccountRequest
	(*ListPurchasesForAccountResponse)(nil),     // 33: primandproper.platform.billing.v1.ListPurchasesForAccountResponse
	(*ArchivePurchaseRequest)(nil),              // 34: primandproper.platform.billing.v1.ArchivePurchaseRequest
	(*ArchivePurchaseResponse)(nil),             // 35: primandproper.platform.billing.v1.ArchivePurchaseResponse
	(*GetTransactionRequest)(nil),               // 36: primandproper.platform.billing.v1.GetTransactionRequest
	(*GetTransactionResponse)(nil),              // 37: primandproper.platform.billing.v1.GetTransactionResponse
	(*ListTransactionsRequest)(nil),             // 38: primandproper.platform.billing.v1.ListTransactionsRequest
	(*ListTransactionsResponse)(nil),            // 39: primandproper.platform.billing.v1.ListTransactionsResponse
	(*ListTransactionsForAccountRequest)(nil),   // 40: primandproper.platform.billing.v1.ListTransactionsForAccountRequest
	(*ListTransactionsForAccountResponse)(nil),  // 41: primandproper.platform.billing.v1.ListTransactionsForAccountResponse
	(*ArchiveTransactionRequest)(nil),           // 42: primandproper.platform.billing.v1.ArchiveTransactionRequest
	(*ArchiveTransactionResponse)(nil),          // 43: primandproper.platform.billing.v1.ArchiveTransactionResponse
	(*timestamppb.Timestamp)(nil),               // 44: google.protobuf.Timestamp
	(*filteringpb.QueryFilter)(nil),             // 45: primandproper.platform.filtering.v1.QueryFilter
	(*filteringpb.Pagination)(nil),              // 46: primandproper.platform.filtering.v1.Pagination
}
var file_primandproper_platform_billing_v1_billing_proto_depIdxs = []int32{
	44, // 0: primandproper.platform.billing.v1.Product.created_at:type_name -> google.protobuf.Timestamp
	44, // 1: primandproper.platform.billing.v1.Product.last_updated_at:type_name -> google.protobuf.Timestamp
	44, // 2: primandproper.platform.billing.v1.Product.archived_at:type_name -> google.protobuf.Timestamp
	0,  // 3: primandproper.platform.billing.v1.Product.kind:type_name -> primandproper.platform.billing.v1.ProductKind
	44, // 4: primandproper.platform.billing.v1.Subscription.created_at:type_name -> google.protobuf.Timestamp
	44, // 5: primandproper.platform.billing.v1.Subscription.current_period_start:type_name -> google.protobuf.Timestamp
	44, // 6: primandproper.platform.billing.v1.Subscription.current_period_end:type_name -> google.protobuf.Timestamp
	44, // 7: primandproper.platform.billing.v1.Subscription.last_updated_at:type_name -> google.protobuf.Timestamp
	44, // 8: primandproper.platform.billing.v1.Subscription.archived_at:type_name -> google.protobuf.Timestamp
	44, // 9: primandproper.platform.billing.v1.Purchase.created_at:type_name -> google.protobuf.Timestamp
	44, // 10: primandproper.platform.billing.v1.Purchase.completed_at:type_name -> google.protobuf.Timestamp
	44, // 11: primandproper.platform.billing.v1.Purchase.last_updated_at:type_name -> google.protobuf.Timestamp
	44, // 12: primandproper.platform.billing.v1.Purchase.archived_at:type_name -> google.protobuf.Timestamp
	44, // 13: primandproper.platform.billing.v1.Transaction.created_at:type_name -> google.protobuf.Timestamp
	44, // 14: primandproper.platform.billing.v1.Transaction.last_updated_at:type_name -> google.protobuf.Timestamp
	44, // 15: primandproper.platform.billing.v1.Transaction.archived_at:type_name -> google.protobuf.Timestamp
	1,  // 16: primandproper.platform.billing.v1.Transaction.status:type_name -> primandproper.platform.billing.v1.TransactionStatus
	0,  // 17: primandproper.platform.billing.v1.ProductCreationInput.kind:type_name -> primandproper.platform.billing.v1.ProductKind
	0,  // 18: primandproper.platform.billing.v1.ProductUpdateInput.kind:type_name -> primandproper.platform.billing.v1.ProductKind
	6,  // 19: primandproper.platform.billing.v1.CreateProductRequest.input:type_name -> primandproper.platform.billing.v1.ProductCreationInput
	2,  // 20: primandproper.platform.billing.v1.CreateProductResponse.result:type_name -> primandproper.platform.billing.v1.Product
	2,  // 21: primandproper.platform.billing.v1.GetProductResponse.result:type_name -> primandproper.platform.billing.v1.Product
	45, // 22: primandproper.platform.billing.v1.ListProductsRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	46, // 23: primandproper.platform.billing.v1.ListProductsResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	2,  // 24: primandproper.platform.billing.v1.ListProductsResponse.results:type_name -> primandproper.platform.billing.v1.Product
	7,  // 25: primandproper.platform.billing.v1.UpdateProductRequest.input:type_name -> primandproper.platform.billing.v1.ProductUpdateInput
	2,  // 26: primandproper.platform.billing.v1.UpdateProductResponse.result:type_name -> primandproper.platform.billing.v1.Product
	3,  // 27: primandproper.platform.billing.v1.GetSubscriptionResponse.result:type_name -> primandproper.platform.billing.v1.Subscription
	45, // 28: primandproper.platform.billing.v1.ListSubscriptionsRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	46, // 29: primandproper.platform.billing.v1.ListSubscriptionsResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	3,  // 30: primandproper.platform.billing.v1.ListSubscriptionsResponse.results:type_name -> primandproper.platform.billing.v1.Subscription
	45, // 31: primandproper.platform.billing.v1.ListSubscriptionsForAccountRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	46, // 32: primandproper.platform.billing.v1.ListSubscriptionsForAccountResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	3,  // 33: primandproper.platform.billing.v1.ListSubscriptionsForAccountResponse.results:type_name -> primandproper.platform.billing.v1.Subscription
	45, // 34: primandproper.platform.billing.v1.ListCurrentSubscriptionsRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	46, // 35: primandproper.platform.billing.v1.ListCurrentSubscriptionsResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	3,  // 36: primandproper.platform.billing.v1.ListCurrentSubscriptionsResponse.results:type_name -> primandproper.platform.billing.v1.Subscription
	4,  // 37: primandproper.platform.billing.v1.GetPurchaseResponse.result:type_name -> primandproper.platform.billing.v1.Purchase
	45, // 38: primandproper.platform.billing.v1.ListPurchasesRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	46, // 39: primandproper.platform.billing.v1.ListPurchasesResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	4,  // 40: primandproper.platform.billing.v1.ListPurchasesResponse.results:type_name -> primandproper.platform.billing.v1.Purchase
	45, // 41: primandproper.platform.billing.v1.ListPurchasesForAccountRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	46, // 42: primandproper.platform.billing.v1.ListPurchasesForAccountResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	4,  // 43: primandproper.platform.billing.v1.ListPurchasesForAccountResponse.results:type_name -> primandproper.platform.billing.v1.Purchase
	5,  // 44: primandproper.platform.billing.v1.GetTransactionResponse.result:type_name -> primandproper.platform.billing.v1.Transaction
	45, // 45: primandproper.platform.billing.v1.ListTransactionsRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	46, // 46: primandproper.platform.billing.v1.ListTransactionsResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	5,  // 47: primandproper.platform.billing.v1.ListTransactionsResponse.results:type_name -> primandproper.platform.billing.v1.Transaction
	45, // 48: primandproper.platform.billing.v1.ListTransactionsForAccountRequest.filter:type_name -> primandproper.platform.filtering.v1.QueryFilter
	46, // 49: primandproper.platform.billing.v1.ListTransactionsForAccountResponse.pagination:type_name -> primandproper.platform.filtering.v1.Pagination
	5,  // 50: primandproper.platform.billing.v1.ListTransactionsForAccountResponse.results:type_name -> primandproper.platform.billing.v1.Transaction
	8,  // 51: primandproper.platform.billing.v1.BillingService.CreateProduct:input_type -> primandproper.platform.billing.v1.CreateProductRequest
	10, // 52: primandproper.platform.billing.v1.BillingService.GetProduct:input_type -> primandproper.platform.billing.v1.GetProductRequest
	12, // 53: primandproper.platform.billing.v1.BillingService.ListProducts:input_type -> primandproper.platform.billing.v1.ListProductsRequest
	14, // 54: primandproper.platform.billing.v1.BillingService.UpdateProduct:input_type -> primandproper.platform.billing.v1.UpdateProductRequest
	16, // 55: primandproper.platform.billing.v1.BillingService.ArchiveProduct:input_type -> primandproper.platform.billing.v1.ArchiveProductRequest
	18, // 56: primandproper.platform.billing.v1.BillingService.GetSubscription:input_type -> primandproper.platform.billing.v1.GetSubscriptionRequest
	20, // 57: primandproper.platform.billing.v1.BillingService.ListSubscriptions:input_type -> primandproper.platform.billing.v1.ListSubscriptionsRequest
	22, // 58: primandproper.platform.billing.v1.BillingService.ListSubscriptionsForAccount:input_type -> primandproper.platform.billing.v1.ListSubscriptionsForAccountRequest
	24, // 59: primandproper.platform.billing.v1.BillingService.ListCurrentSubscriptions:input_type -> primandproper.platform.billing.v1.ListCurrentSubscriptionsRequest
	26, // 60: primandproper.platform.billing.v1.BillingService.ArchiveSubscription:input_type -> primandproper.platform.billing.v1.ArchiveSubscriptionRequest
	28, // 61: primandproper.platform.billing.v1.BillingService.GetPurchase:input_type -> primandproper.platform.billing.v1.GetPurchaseRequest
	30, // 62: primandproper.platform.billing.v1.BillingService.ListPurchases:input_type -> primandproper.platform.billing.v1.ListPurchasesRequest
	32, // 63: primandproper.platform.billing.v1.BillingService.ListPurchasesForAccount:input_type -> primandproper.platform.billing.v1.ListPurchasesForAccountRequest
	34, // 64: primandproper.platform.billing.v1.BillingService.ArchivePurchase:input_type -> primandproper.platform.billing.v1.ArchivePurchaseRequest
	36, // 65: primandproper.platform.billing.v1.BillingService.GetTransaction:input_type -> primandproper.platform.billing.v1.GetTransactionRequest
	38, // 66: primandproper.platform.billing.v1.BillingService.ListTransactions:input_type -> primandproper.platform.billing.v1.ListTransactionsRequest
	40, // 67: primandproper.platform.billing.v1.BillingService.ListTransactionsForAccount:input_type -> primandproper.platform.billing.v1.ListTransactionsForAccountRequest
	42, // 68: primandproper.platform.billing.v1.BillingService.ArchiveTransaction:input_type -> primandproper.platform.billing.v1.ArchiveTransactionRequest
	9,  // 69: primandproper.platform.billing.v1.BillingService.CreateProduct:output_type -> primandproper.platform.billing.v1.CreateProductResponse
	11, // 70: primandproper.platform.billing.v1.BillingService.GetProduct:output_type -> primandproper.platform.billing.v1.GetProductResponse
	13, // 71: primandproper.platform.billing.v1.BillingService.ListProducts:output_type -> primandproper.platform.billing.v1.ListProductsResponse
	15, // 72: primandproper.platform.billing.v1.BillingService.UpdateProduct:output_type -> primandproper.platform.billing.v1.UpdateProductResponse
	17, // 73: primandproper.platform.billing.v1.BillingService.ArchiveProduct:output_type -> primandproper.platform.billing.v1.ArchiveProductResponse
	19, // 74: primandproper.platform.billing.v1.BillingService.GetSubscription:output_type -> primandproper.platform.billing.v1.GetSubscriptionResponse
	21, // 75: primandproper.platform.billing.v1.BillingService.ListSubscriptions:output_type -> primandproper.platform.billing.v1.ListSubscriptionsResponse
	23, // 76: primandproper.platform.billing.v1.BillingService.ListSubscriptionsForAccount:output_type -> primandproper.platform.billing.v1.ListSubscriptionsForAccountResponse
	25, // 77: primandproper.platform.billing.v1.BillingService.ListCurrentSubscriptions:output_type -> primandproper.platform.billing.v1.ListCurrentSubscriptionsResponse
	27, // 78: primandproper.platform.billing.v1.BillingService.ArchiveSubscription:output_type -> primandproper.platform.billing.v1.ArchiveSubscriptionResponse
	29, // 79: primandproper.platform.billing.v1.BillingService.GetPurchase:output_type -> primandproper.platform.billing.v1.GetPurchaseResponse
	31, // 80: primandproper.platform.billing.v1.BillingService.ListPurchases:output_type -> primandproper.platform.billing.v1.ListPurchasesResponse
	33, // 81: primandproper.platform.billing.v1.BillingService.ListPurchasesForAccount:output_type -> primandproper.platform.billing.v1.ListPurchasesForAccountResponse
	35, // 82: primandproper.platform.billing.v1.BillingService.ArchivePurchase:output_type -> primandproper.platform.billing.v1.ArchivePurchaseResponse
	37, // 83: primandproper.platform.billing.v1.BillingService.GetTransaction:output_type -> primandproper.platform.billing.v1.GetTransactionResponse
	39, // 84: primandproper.platform.billing.v1.BillingService.ListTransactions:output_type -> primandproper.platform.billing.v1.ListTransactionsResponse
	41, // 85: primandproper.platform.billing.v1.BillingService.ListTransactionsForAccount:output_type -> primandproper.platform.billing.v1.ListTransactionsForAccountResponse
	43, // 86: primandproper.platform.billing.v1.BillingService.ArchiveTransaction:output_type -> primandproper.platform.billing.v1.ArchiveTransactionResponse
	69, // [69:87] is the sub-list for method output_type
	51, // [51:69] is the sub-list for method input_type
	51, // [51:51] is the sub-list for extension type_name
	51, // [51:51] is the sub-list for extension extendee
	0,  // [0:51] is the sub-list for field type_name
}

func init() { file_primandproper_platform_billing_v1_billing_proto_init() }
func file_primandproper_platform_billing_v1_billing_proto_init() {
	if File_primandproper_platform_billing_v1_billing_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_primandproper_platform_billing_v1_billing_proto_rawDesc), len(file_primandproper_platform_billing_v1_billing_proto_rawDesc)),
			NumEnums:      2,
			NumMessages:   42,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_primandproper_platform_billing_v1_billing_proto_goTypes,
		DependencyIndexes: file_primandproper_platform_billing_v1_billing_proto_depIdxs,
		EnumInfos:         file_primandproper_platform_billing_v1_billing_proto_enumTypes,
		MessageInfos:      file_primandproper_platform_billing_v1_billing_proto_msgTypes,
	}.Build()
	File_primandproper_platform_billing_v1_billing_proto = out.File
	file_primandproper_platform_billing_v1_billing_proto_goTypes = nil
	file_primandproper_platform_billing_v1_billing_proto_depIdxs = nil
}
