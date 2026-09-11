package grpc

import (
	"github.com/primandproper/platform-go/v14/billing/billingpb"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// The permissions this service's methods require, in authorization's
// vocabulary.
//
// They are declared here rather than in the authorization package because
// authorization is a primitive: it owns what a Permission *is* — a string, a
// set, a role that inherits — and owns no domain's names.
//
// The strings are dotted and namespaced so that a consumer composing several
// domains' fragments cannot have two of them collide on "read". They are values
// rather than an enum because a consumer's policy is data — a YAML file, a table
// of roles — and it has to be able to name one without importing Go.
//
// The shape of this file is one distinction repeated three times: reading your
// own rows and enumerating everybody's are not the same grant. A customer
// portal needs the first and an operator console needs the second, and a policy
// that could not separate them would hand out the customer ledger to get
// somebody their own invoices.
const (
	// PermissionReadProducts covers reading a product and paging the catalog.
	//
	// One permission for the get and the list, because the catalog is
	// scope-wide: it is what the deployment sells, the same for everybody in
	// the scope, and a grant that separated them would let a consumer allow
	// enumeration while forbidding the read it enumerates into. Most
	// deployments grant this to every signed-in caller.
	PermissionReadProducts authorization.Permission = "billing.products.read"

	// PermissionCreateProducts covers stocking the catalog.
	PermissionCreateProducts authorization.Permission = "billing.products.create"

	// PermissionUpdateProducts covers revising a product, repricing included.
	//
	// Separate from PermissionCreateProducts because they are different acts on
	// a live catalog: adding a plan nobody is on yet is not the same as
	// changing the price of one people are buying.
	PermissionUpdateProducts authorization.Permission = "billing.products.update"

	// PermissionArchiveProducts covers withdrawing a product from sale.
	PermissionArchiveProducts authorization.Permission = "billing.products.archive"

	// PermissionReadSubscriptions covers reading one subscription and paging an
	// account's, current or lapsed.
	//
	// The grant is on the method and says the caller may perform this kind of
	// read; whose subscriptions they may perform it against is
	// [AccountAuthorizer]'s, asked inside the handler where the store is. Both
	// have to say yes.
	PermissionReadSubscriptions authorization.Permission = "billing.subscriptions.read"

	// PermissionListAllSubscriptions covers paging every subscription in the
	// scope, which is the operator's read and answers to no account.
	PermissionListAllSubscriptions authorization.Permission = "billing.subscriptions.list_all"

	// PermissionArchiveSubscriptions covers retiring a subscription
	// administratively.
	//
	// It is not a cancellation and must not be granted as one: cancelling is a
	// fact the payment provider reports and arrives here as a status. This
	// hides the row.
	PermissionArchiveSubscriptions authorization.Permission = "billing.subscriptions.archive"

	// PermissionReadPurchases covers reading one purchase and paging an
	// account's. See PermissionReadSubscriptions on the second half of the
	// question.
	PermissionReadPurchases authorization.Permission = "billing.purchases.read"

	// PermissionListAllPurchases covers paging every purchase in the scope.
	PermissionListAllPurchases authorization.Permission = "billing.purchases.list_all"

	// PermissionArchivePurchases covers retiring a purchase administratively. It
	// is not a refund — a refund is a transaction of its own.
	PermissionArchivePurchases authorization.Permission = "billing.purchases.archive"

	// PermissionReadTransactions covers reading one ledger row and paging an
	// account's.
	PermissionReadTransactions authorization.Permission = "billing.transactions.read"

	// PermissionListAllTransactions covers paging the whole ledger, which is
	// what a reconciliation walks. It is the sharpest read grant in this file:
	// the page is every account's money, in creation order.
	PermissionListAllTransactions authorization.Permission = "billing.transactions.list_all"

	// PermissionArchiveTransactions covers retiring a ledger row
	// administratively — the row written in error, a test charge, a duplicate
	// that predates the uniqueness.
	PermissionArchiveTransactions authorization.Permission = "billing.transactions.archive"
)

// Permissions is the default map from method name to what it requires: every
// RPC this service declares, and nothing else.
//
// Every method is in it. There is no second set of methods that require nothing
// — this service serves no RPC a caller reaches without a grant, the catalog
// included — so a method missing from this map is a bug rather than a decision,
// and the suite reads the service descriptor rather than a list in order to say
// so.
//
// The keys are the generated full method name constants rather than strings, so
// an RPC renamed in the .proto is a compile error here instead of a method that
// silently requires nothing.
//
// It returns a fresh map each call, so a consumer composing it into their own
// policy and then overriding an entry is editing their copy.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		billingpb.BillingService_CreateProduct_FullMethodName:  {PermissionCreateProducts},
		billingpb.BillingService_GetProduct_FullMethodName:     {PermissionReadProducts},
		billingpb.BillingService_ListProducts_FullMethodName:   {PermissionReadProducts},
		billingpb.BillingService_UpdateProduct_FullMethodName:  {PermissionUpdateProducts},
		billingpb.BillingService_ArchiveProduct_FullMethodName: {PermissionArchiveProducts},

		billingpb.BillingService_GetSubscription_FullMethodName:             {PermissionReadSubscriptions},
		billingpb.BillingService_ListSubscriptionsForAccount_FullMethodName: {PermissionReadSubscriptions},
		billingpb.BillingService_ListCurrentSubscriptions_FullMethodName:    {PermissionReadSubscriptions},
		billingpb.BillingService_ListSubscriptions_FullMethodName:           {PermissionListAllSubscriptions},
		billingpb.BillingService_ArchiveSubscription_FullMethodName:         {PermissionArchiveSubscriptions},

		billingpb.BillingService_GetPurchase_FullMethodName:             {PermissionReadPurchases},
		billingpb.BillingService_ListPurchasesForAccount_FullMethodName: {PermissionReadPurchases},
		billingpb.BillingService_ListPurchases_FullMethodName:           {PermissionListAllPurchases},
		billingpb.BillingService_ArchivePurchase_FullMethodName:         {PermissionArchivePurchases},

		billingpb.BillingService_GetTransaction_FullMethodName:             {PermissionReadTransactions},
		billingpb.BillingService_ListTransactionsForAccount_FullMethodName: {PermissionReadTransactions},
		billingpb.BillingService_ListTransactions_FullMethodName:           {PermissionListAllTransactions},
		billingpb.BillingService_ArchiveTransaction_FullMethodName:         {PermissionArchiveTransactions},
	}
}

// Require declares every method of this service on a requirements builder.
//
// It is one line over [Permissions] today and is the exported name anyway,
// because authorization/grpc is fail-closed: a method declared nowhere is
// denied, and what a consumer needs is a call that stays correct when this
// service's method set changes.
//
// A nil builder is tolerated and returns nil, so composing several domains'
// fragments in a chain does not need a nil check per link.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	return b.RequireAll(Permissions())
}
