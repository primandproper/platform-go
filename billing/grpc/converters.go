package grpc

import (
	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/billingpb"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// The conversions between billing's types and the generated messages.
//
// The four To-proto functions are exported because a consumer composing billing
// into a larger response — an account page assembling a plan, its invoices and
// its ledger — otherwise writes the same eleven assignments and gets one of them
// wrong. The From-proto ones are not: they read a request, and a request is this
// service's to read.
//
// Two rules run through all of them. The nullable times stay unset rather than
// becoming the zero timestamp, because a client rendering "last updated" wants
// to know there was no update and 1970 is not that answer. And no message
// carries a scope, so no converter reads or writes one — the scope is bound off
// the caller, and a Scope on a value handed to a write is overwritten by the
// store with the argument it was given.

// ProductToProto renders a product for the wire.
func ProductToProto(p *billing.Product) *billingpb.Product {
	if p == nil {
		return nil
	}

	out := &billingpb.Product{
		CreatedAt:             timestamppb.New(p.CreatedAt),
		Id:                    p.ID,
		Name:                  p.Name,
		Description:           p.Description,
		Kind:                  kindToProto(p.Kind),
		Currency:              p.Currency,
		ExternalProductId:     p.ExternalProductID,
		AmountCents:           p.AmountCents,
		BillingIntervalMonths: p.BillingIntervalMonths,
	}

	if p.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*p.LastUpdatedAt)
	}

	if p.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*p.ArchivedAt)
	}

	return out
}

// ProductsToProto renders a page of products.
func ProductsToProto(products []*billing.Product) []*billingpb.Product {
	out := make([]*billingpb.Product, 0, len(products))
	for _, p := range products {
		out = append(out, ProductToProto(p))
	}

	return out
}

// SubscriptionToProto renders a subscription for the wire.
//
// The status crosses as the string capitalism stores it — "active", "past_due"
// — rather than as a generated enum. billing.proto's opening comment carries
// the reason: the set belongs to a different module, and a status it added and
// this store wrote would render as UNSPECIFIED here until this module caught up.
// A string cannot be behind.
func SubscriptionToProto(s *billing.Subscription) *billingpb.Subscription {
	if s == nil {
		return nil
	}

	out := &billingpb.Subscription{
		CreatedAt:              timestamppb.New(s.CreatedAt),
		CurrentPeriodStart:     timestamppb.New(s.CurrentPeriodStart),
		CurrentPeriodEnd:       timestamppb.New(s.CurrentPeriodEnd),
		Id:                     s.ID,
		BelongsToAccount:       s.BelongsToAccount,
		ProductId:              s.ProductID,
		ExternalSubscriptionId: s.ExternalSubscriptionID,
		Status:                 s.Status.String(),
	}

	if s.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*s.LastUpdatedAt)
	}

	if s.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*s.ArchivedAt)
	}

	return out
}

// SubscriptionsToProto renders a page of subscriptions.
func SubscriptionsToProto(subscriptions []*billing.Subscription) []*billingpb.Subscription {
	out := make([]*billingpb.Subscription, 0, len(subscriptions))
	for _, s := range subscriptions {
		out = append(out, SubscriptionToProto(s))
	}

	return out
}

// PurchaseToProto renders a purchase for the wire.
func PurchaseToProto(p *billing.Purchase) *billingpb.Purchase {
	if p == nil {
		return nil
	}

	out := &billingpb.Purchase{
		CreatedAt:             timestamppb.New(p.CreatedAt),
		Id:                    p.ID,
		BelongsToAccount:      p.BelongsToAccount,
		ProductId:             p.ProductID,
		ExternalTransactionId: p.ExternalTransactionID,
		Currency:              p.Currency,
		AmountCents:           p.AmountCents,
	}

	// completed_at is the whole lifecycle a purchase has, and unset is what
	// "still outstanding" looks like. It is the one nullable time on this
	// message a client acts on rather than displays.
	if p.CompletedAt != nil {
		out.CompletedAt = timestamppb.New(*p.CompletedAt)
	}

	if p.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*p.LastUpdatedAt)
	}

	if p.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*p.ArchivedAt)
	}

	return out
}

// PurchasesToProto renders a page of purchases.
func PurchasesToProto(purchases []*billing.Purchase) []*billingpb.Purchase {
	out := make([]*billingpb.Purchase, 0, len(purchases))
	for _, p := range purchases {
		out = append(out, PurchaseToProto(p))
	}

	return out
}

// TransactionToProto renders a ledger row for the wire.
func TransactionToProto(t *billing.Transaction) *billingpb.Transaction {
	if t == nil {
		return nil
	}

	out := &billingpb.Transaction{
		CreatedAt:             timestamppb.New(t.CreatedAt),
		Id:                    t.ID,
		BelongsToAccount:      t.BelongsToAccount,
		SubscriptionId:        t.SubscriptionID,
		PurchaseId:            t.PurchaseID,
		ExternalTransactionId: t.ExternalTransactionID,
		Status:                transactionStatusToProto(t.Status),
		Currency:              t.Currency,
		AmountCents:           t.AmountCents,
	}

	if t.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*t.LastUpdatedAt)
	}

	if t.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*t.ArchivedAt)
	}

	return out
}

// TransactionsToProto renders a page of ledger rows.
func TransactionsToProto(transactions []*billing.Transaction) []*billingpb.Transaction {
	out := make([]*billingpb.Transaction, 0, len(transactions))
	for _, t := range transactions {
		out = append(out, TransactionToProto(t))
	}

	return out
}

// kindToProto renders a product kind.
//
// A kind this package does not implement renders as unspecified rather than
// panicking, and it is unreachable from a stored row: the store validates the
// kind on every write, so the column holds one of the two.
func kindToProto(k billing.Kind) billingpb.ProductKind {
	switch k {
	case billing.KindRecurring:
		return billingpb.ProductKind_PRODUCT_KIND_RECURRING
	case billing.KindOneTime:
		return billingpb.ProductKind_PRODUCT_KIND_ONE_TIME
	default:
		return billingpb.ProductKind_PRODUCT_KIND_UNSPECIFIED
	}
}

// kindFromProto reads a product kind off a request.
//
// The unspecified case becomes the empty Kind rather than a default, so a
// request that named no kind is refused by the store's own validation — with
// billing.ErrInvalidKind, naming the field — instead of quietly becoming a
// one-time product. Choosing a default here would be this package deciding what
// a deployment sells.
func kindFromProto(k billingpb.ProductKind) billing.Kind {
	switch k {
	case billingpb.ProductKind_PRODUCT_KIND_RECURRING:
		return billing.KindRecurring
	case billingpb.ProductKind_PRODUCT_KIND_ONE_TIME:
		return billing.KindOneTime
	case billingpb.ProductKind_PRODUCT_KIND_UNSPECIFIED:
		return billing.Kind("")
	default:
		return billing.Kind("")
	}
}

// transactionStatusToProto renders a ledger row's status. As with the kind, the
// unspecified case is unreachable from a stored row.
func transactionStatusToProto(s billing.TransactionStatus) billingpb.TransactionStatus {
	switch s {
	case billing.TransactionPending:
		return billingpb.TransactionStatus_TRANSACTION_STATUS_PENDING
	case billing.TransactionSucceeded:
		return billingpb.TransactionStatus_TRANSACTION_STATUS_SUCCEEDED
	case billing.TransactionFailed:
		return billingpb.TransactionStatus_TRANSACTION_STATUS_FAILED
	case billing.TransactionRefunded:
		return billingpb.TransactionStatus_TRANSACTION_STATUS_REFUNDED
	default:
		return billingpb.TransactionStatus_TRANSACTION_STATUS_UNSPECIFIED
	}
}

// productFromCreationInput reads a request into the entity the store stores.
//
// A nil message is nil rather than an empty product, so a request that named no
// input is refused as malformed instead of being stored as a product with no
// name — which the store would refuse anyway, with a message about the name
// rather than about the request.
//
// No scope is set. The store binds it from the argument it is handed, which is
// the derivation billing.Store's documentation exists to rule out doing here.
func productFromCreationInput(in *billingpb.ProductCreationInput) *billing.Product {
	if in == nil {
		return nil
	}

	return &billing.Product{
		Name:                  in.GetName(),
		Description:           in.GetDescription(),
		Kind:                  kindFromProto(in.GetKind()),
		Currency:              in.GetCurrency(),
		ExternalProductID:     in.GetExternalProductId(),
		AmountCents:           in.GetAmountCents(),
		BillingIntervalMonths: in.GetBillingIntervalMonths(),
	}
}

// productFromUpdateInput reads a revision into the entity the store's update
// takes, under the id the request named.
//
// It restates all seven revisable fields because the store's update assigns all
// seven together — the row is one statement of what is on sale, and a revision
// able to write half of it would leave a price and a recurrence disagreeing.
// Nothing else about the row is reachable from here: the created time, the
// archived time and the scope are not on the message and not on this value.
func productFromUpdateInput(productID string, in *billingpb.ProductUpdateInput) *billing.Product {
	if in == nil {
		return nil
	}

	return &billing.Product{
		ID:                    productID,
		Name:                  in.GetName(),
		Description:           in.GetDescription(),
		Kind:                  kindFromProto(in.GetKind()),
		Currency:              in.GetCurrency(),
		ExternalProductID:     in.GetExternalProductId(),
		AmountCents:           in.GetAmountCents(),
		BillingIntervalMonths: in.GetBillingIntervalMonths(),
	}
}
